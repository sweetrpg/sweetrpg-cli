package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	catalogvo "github.com/sweetrpg/catalog-objects.go/vo"
)

// fixtureServer is a scripted catalog-api: it records the last request and
// replies with a canned status + body.
type fixtureServer struct {
	t          *testing.T
	status     int
	body       string
	gotMethod  string
	gotPath    string
	gotRawQry  string
	gotBody    []byte
	gotHeaders http.Header
}

func newFixture(t *testing.T, status int, body string) (*fixtureServer, *Client) {
	t.Helper()
	f := &fixtureServer{t: t, status: status, body: body}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		f.gotMethod, f.gotPath, f.gotRawQry = r.Method, r.URL.Path, r.URL.RawQuery
		f.gotBody, f.gotHeaders = raw, r.Header.Clone()
		w.Header().Set("Content-Type", MediaType)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, func(context.Context) (string, error) { return "test-token", nil })
	if err != nil {
		t.Fatal(err)
	}
	return f, c
}

const oneVolumeJSON = `{"data":{"type":"volume","id":"abc123","attributes":{"title":"Dungeons of Dread","description":"","notes":"","format":"","coverAssetId":"","properties":null,"tags":null,"created_by":"","created_at":"0001-01-01T00:00:00Z","updated_at":"0001-01-01T00:00:00Z"}}}`

func listVolumesJSON(ids ...string) string {
	out := `{"data":[`
	for i, id := range ids {
		if i > 0 {
			out += ","
		}
		out += `{"type":"volume","id":"` + id + `","attributes":{"title":"Vol ` + id + `"}}`
	}
	return out + "]}"
}

func TestGetDecodesJSONAPIDocument(t *testing.T) {
	_, c := newFixture(t, http.StatusOK, oneVolumeJSON)

	vol, err := Get[catalogvo.VolumeVO](context.Background(), c, "volumes", "abc123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vol.ID != "abc123" || vol.Title != "Dungeons of Dread" {
		t.Errorf("decoded %+v", vol)
	}
}

func TestGetNotFoundMapsToAPIError(t *testing.T) {
	_, c := newFixture(t, http.StatusNotFound, "{}")

	_, err := Get[catalogvo.VolumeVO](context.Background(), c, "volumes", "nope")
	if !IsNotFound(err) {
		t.Fatalf("want not-found, got %v", err)
	}
}

func TestListDecodesCollectionAndBuildsQuery(t *testing.T) {
	f, c := newFixture(t, http.StatusOK, listVolumesJSON("a1", "b2"))

	opts := ListOptions{
		Filters: []Filter{{Field: "format", Values: []string{"pdf", "hardcover"}}},
		Limit:   100,
		Start:   25,
	}
	vols, err := List[catalogvo.VolumeVO](context.Background(), c, "volumes", opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vols) != 2 || vols[0].ID != "a1" || vols[1].Title != "Vol b2" {
		t.Errorf("decoded %d volumes", len(vols))
	}
	q := f.gotRawQry
	for _, want := range []string{"filter%5Bformat%5D=pdf%2Chardcover", "page%5Blimit%5D=100", "page%5Bstart%5D=25"} {
		if !contains(q, want) {
			t.Errorf("query %q missing %q", q, want)
		}
	}
}

func TestListEmptyPageReturnsEmptySliceNotNil(t *testing.T) {
	_, c := newFixture(t, http.StatusOK, `{"data":[]}`)

	vols, err := List[catalogvo.VolumeVO](context.Background(), c, "volumes", ListOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vols == nil || len(vols) != 0 {
		t.Errorf("want empty non-nil slice, got %#v", vols)
	}
}

func TestSearchHitsSearchEndpoint(t *testing.T) {
	f, c := newFixture(t, http.StatusOK, listVolumesJSON())

	_, err := Search[catalogvo.PublisherVO](context.Background(), c, "publishers", "acme")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.gotPath != "/publishers/search" || !contains(f.gotRawQry, "q=acme") {
		t.Errorf("path=%q query=%q", f.gotPath, f.gotRawQry)
	}
}

func TestCreateSendsPlainJSONAndParsesResponse(t *testing.T) {
	f, c := newFixture(t, http.StatusCreated, oneVolumeJSON)

	in := &catalogvo.VolumeVO{ID: "", Title: "Dungeons of Dread"}
	out, err := Create(context.Background(), c, "volumes", in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.ID != "abc123" {
		t.Errorf("created ID %q", out.ID)
	}
	if got := f.gotHeaders.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
	var sent map[string]any
	if err := json.Unmarshal(f.gotBody, &sent); err != nil {
		t.Fatalf("body not plain JSON: %s", f.gotBody)
	}
	if sent["title"] != "Dungeons of Dread" {
		t.Errorf("sent body %s", f.gotBody)
	}
	if _, hasData := sent["data"]; hasData {
		t.Error("create must not send a JSON:API envelope")
	}
}

func TestPatchLiveWriteReturnsRecordAndDisposition(t *testing.T) {
	f, c := newFixture(t, http.StatusOK, oneVolumeJSON)

	live, d, err := Patch[catalogvo.VolumeVO](context.Background(), c, "volumes", "abc123",
		map[string]any{"title": "Dungeons of Dread"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.Submitted {
		t.Error("200 must be a live write")
	}
	if live == nil || live.Title != "Dungeons of Dread" {
		t.Errorf("live record %+v", live)
	}
	var sent map[string]any
	_ = json.Unmarshal(f.gotBody, &sent)
	if sent["title"] != "Dungeons of Dread" || len(sent) != 1 {
		t.Errorf("flat patch body %s", f.gotBody)
	}
	if f.gotPath != "/volumes/abc123" || f.gotMethod != "PATCH" {
		t.Errorf("%s %s", f.gotMethod, f.gotPath)
	}
}

func TestPatchSubmitterGetsProposedChangeDisposition(t *testing.T) {
	_, c := newFixture(t, http.StatusAccepted,
		`{"version":4,"state":"submitted","message":"Change submitted for review"}`)

	live, d, err := Patch[catalogvo.VolumeVO](context.Background(), c, "volumes", "abc123",
		map[string]any{"title": "Renamed"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !d.Submitted || d.Version != 4 || d.State != "submitted" || d.Message != "Change submitted for review" {
		t.Errorf("disposition %+v", d)
	}
	if live != nil {
		t.Error("submitted patch must not return a live record")
	}
}

func TestDeleteIssues204AgainstRightPath(t *testing.T) {
	f, c := newFixture(t, http.StatusNoContent, "")

	if err := Delete(context.Background(), c, "publishers", "p1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.gotMethod != "DELETE" || f.gotPath != "/publishers/p1" {
		t.Errorf("%s %s", f.gotMethod, f.gotPath)
	}
}

func TestErrorBodyIsParsedIntoAPIError(t *testing.T) {
	_, c := newFixture(t, http.StatusBadRequest,
		`{"error":"invalid_request","message":"name is required"}`)

	_, err := Create[catalogvo.PublisherVO](context.Background(), c, "publishers",
		&catalogvo.PublisherVO{})
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("want *APIError, got %v", err)
	}
	if apiErr.Code != "invalid_request" || apiErr.Message != "name is required" {
		t.Errorf("parsed %+v", apiErr)
	}
	if IsAuthError(err) {
		t.Error("400 is not an auth error")
	}
}

func TestAuthErrorsAreClassified(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		_, c := newFixture(t, status, `{"error":"forbidden"}`)
		_, err := Get[catalogvo.VolumeVO](context.Background(), c, "volumes", "x")
		if !IsAuthError(err) {
			t.Errorf("status %d not classified as auth error: %v", status, err)
		}
	}
}

func TestBearerTokenAttached(t *testing.T) {
	f, c := newFixture(t, http.StatusOK, oneVolumeJSON)

	if _, err := Get[catalogvo.VolumeVO](context.Background(), c, "volumes", "abc123"); err != nil {
		t.Fatal(err)
	}
	if got := f.gotHeaders.Get("Authorization"); got != "Bearer test-token" {
		t.Errorf("Authorization = %q", got)
	}
}

func TestLookupRejectsUnknownEntity(t *testing.T) {
	if _, err := Lookup("volumez"); err == nil {
		t.Fatal("want error for unknown entity type")
	}
	for name := range Entities {
		if _, err := Lookup(name); err != nil {
			t.Errorf("registry entry %q failed lookup: %v", name, err)
		}
	}
}

func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}

func TestAPIErrorParsesJSONAPIErrorDetail(t *testing.T) {
	f, c := newFixture(t, http.StatusUnprocessableEntity,
		`{"errors":[{"detail":"submission cap reached for this record"}]}`)
	_ = f
	_, err := Get[catalogvo.VolumeVO](context.Background(), c, "volumes", "x")
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("want *APIError, got %T", err)
	}
	if apiErr.Message != "submission cap reached for this record" {
		t.Errorf("message = %q", apiErr.Message)
	}
}
