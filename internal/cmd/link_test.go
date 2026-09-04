package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sweetrpg/sweetrpg-cli/internal/client"
)

const (
	linkVolumeID   = "507f1f77bcf86cd799439011"
	linkPubID      = "aaaaaaaaaaaaaaaaaaaaaaaa"
	linkOtherPubID = "dddddddddddddddddddddddd"
	linkPersonID   = "bbbbbbbbbbbbbbbbbbbbbbbb"
)

func linkedVolumeJSON(pubs string) string {
	return `{"data":{"type":"volume","id":"` + linkVolumeID + `","attributes":{"title":"Dungeon World"},"relationships":{"publisher":{"data":[` + pubs + `]}}}}`
}

func pubRel(id string) string {
	return `{"type":"publisher","id":"` + id + `"}`
}

// statusBody is one scripted response from the fake catalog-api.
type statusBody struct {
	status int
	body   string
}

func ok200(body string) statusBody { return statusBody{http.StatusOK, body} }

// scriptedFixture serves one response per request in order; the last one
// repeats, matching how link flows issue GETs then at most one PATCH.
type scriptedFixture struct {
	requests int
	methods  []string
	paths    []string
	queries  []string
	bodies   []map[string]any
}

func newScriptedFixture(t *testing.T, responses ...statusBody) *scriptedFixture {
	t.Helper()
	f := &scriptedFixture{}
	i := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		f.requests++
		f.methods = append(f.methods, r.Method)
		f.paths = append(f.paths, r.URL.Path)
		f.queries = append(f.queries, r.URL.RawQuery)
		if len(raw) > 0 {
			var m map[string]any
			_ = json.Unmarshal(raw, &m)
			f.bodies = append(f.bodies, m)
		} else {
			f.bodies = append(f.bodies, nil)
		}
		resp := responses[min(i, len(responses)-1)]
		i++
		w.Header().Set("Content-Type", client.MediaType)
		w.WriteHeader(resp.status)
		_, _ = io.WriteString(w, resp.body)
	}))
	t.Cleanup(srv.Close)
	oldBuilder := buildAPIClient
	buildAPIClient = func() (*client.Client, error) {
		c, err := client.New(srv.URL, func(context.Context) (string, error) { return "test-token", nil })
		return c, err
	}
	t.Cleanup(func() { buildAPIClient = oldBuilder })
	resetResolveState(t)
	return f
}

// runLink drives a fresh link/unlink command so --role state never leaks
// between tests.
func runLink(t *testing.T, add bool, role string, args ...string) string {
	t.Helper()
	cmd := newLinkCommand()
	if !add {
		cmd = newUnlinkCommand()
	}
	if role != "" {
		if err := cmd.Flags().Set("role", role); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	cmd.SetContext(context.Background())
	cmd.SetOut(&out)
	if err := cmd.RunE(cmd, args); err != nil {
		t.Fatalf("command failed: %v", err)
	}
	return out.String()
}

func runLinkErr(t *testing.T, add bool, args ...string) error {
	t.Helper()
	cmd := newLinkCommand()
	if !add {
		cmd = newUnlinkCommand()
	}
	var out bytes.Buffer
	cmd.SetContext(context.Background())
	cmd.SetOut(&out)
	return cmd.RunE(cmd, args)
}

func TestLinkVolumePublisherBothOrders(t *testing.T) {
	for _, args := range [][]string{
		{"volume", linkVolumeID, "publisher", linkPubID},
		{"publisher", linkPubID, "volume", linkVolumeID},
	} {
		f := newScriptedFixture(t, ok200(linkedVolumeJSON("")))
		out := runLink(t, true, "", args...)
		if f.requests != 2 || f.methods[0] != "GET" || f.methods[1] != "PATCH" ||
			f.paths[1] != "/volumes/"+linkVolumeID {
			t.Fatalf("unexpected traffic: %v %v", f.methods, f.paths)
		}
		ids := f.bodies[1]["publisherIds"].([]any)
		if len(ids) != 1 || ids[0] != linkPubID {
			t.Errorf("want publisherIds [%s], got %v", linkPubID, f.bodies[1])
		}
		if !strings.Contains(out, "Linked publisher") {
			t.Errorf("missing link confirmation:\n%s", out)
		}
	}
}

func TestLinkVolumeStudioAndSystemPatchTheirFields(t *testing.T) {
	f := newScriptedFixture(t, ok200(linkedVolumeJSON("")))
	runLink(t, true, "", "volume", linkVolumeID, "studio", linkPubID)
	if f.methods[1] != "PATCH" || f.paths[1] != "/volumes/"+linkVolumeID {
		t.Fatalf("unexpected traffic: %v %v", f.methods, f.paths)
	}
	if ids := f.bodies[1]["studioIds"].([]any); len(ids) != 1 || ids[0] != linkPubID {
		t.Errorf("want studioIds [%s], got %v", linkPubID, f.bodies[1])
	}

	f = newScriptedFixture(t, ok200(linkedVolumeJSON("")))
	runLink(t, true, "", "system", linkPubID, "volume", linkVolumeID)
	if ids := f.bodies[1]["systemIds"].([]any); len(ids) != 1 || ids[0] != linkPubID {
		t.Errorf("want systemIds [%s], got %v", linkPubID, f.bodies[1])
	}
}

func TestLinkInvalidPairingIssuesNoRequests(t *testing.T) {
	f := newScriptedFixture(t, ok200("{}"))
	err := runLinkErr(t, true, "person", linkPersonID, "studio", linkPubID)
	if err == nil || !strings.Contains(err.Error(), "aren't supported") ||
		!strings.Contains(err.Error(), "volume") {
		t.Fatalf("want unsupported-pairing error naming counterparts, got %v", err)
	}
	if f.requests != 0 {
		t.Errorf("invalid pairing issued %d requests", f.requests)
	}
}

func TestLinkUnknownTypeIsUsageError(t *testing.T) {
	f := newScriptedFixture(t, ok200("{}"))
	err := runLinkErr(t, true, "widget", "x", "volume", linkVolumeID)
	if err == nil || !strings.Contains(err.Error(), "unknown entity type") {
		t.Fatalf("want unknown-type error, got %v", err)
	}
	if f.requests != 0 {
		t.Errorf("unknown type issued %d requests", f.requests)
	}
}

func TestRelinkExistingPairExitsZeroWithoutPatch(t *testing.T) {
	f := newScriptedFixture(t, ok200(linkedVolumeJSON(pubRel(linkPubID))))
	out := runLink(t, true, "", "volume", linkVolumeID, "publisher", linkPubID)
	if f.requests != 1 {
		t.Errorf("re-link issued %d requests, want only the GET", f.requests)
	}
	if !strings.Contains(out, "already linked") {
		t.Errorf("missing idempotency notice:\n%s", out)
	}
}

func TestUnlinkRemovesPublisherAndPatches(t *testing.T) {
	f := newScriptedFixture(t, ok200(linkedVolumeJSON(pubRel(linkPubID)+","+pubRel(linkOtherPubID))))
	out := runLink(t, false, "", "volume", linkVolumeID, "publisher", linkPubID)
	ids := f.bodies[1]["publisherIds"].([]any)
	if len(ids) != 1 || ids[0] != linkOtherPubID {
		t.Errorf("want remaining publisher kept, got %v", f.bodies[1])
	}
	if !strings.Contains(out, "Unlinked publisher") {
		t.Errorf("missing unlink confirmation:\n%s", out)
	}
}

func TestUnlinkAbsentPairExitsZero(t *testing.T) {
	f := newScriptedFixture(t, ok200(linkedVolumeJSON("")))
	out := runLink(t, false, "", "volume", linkVolumeID, "publisher", linkPubID)
	if f.requests != 1 || !strings.Contains(out, "isn't linked") {
		t.Errorf("want single GET plus notice, requests=%d out=%s", f.requests, out)
	}
}

const (
	contributionListJSON = `{"data":[{"type":"contribution","id":"cccccccccccccccccccccccc","attributes":{"role":"author"},"relationships":{"person":{"data":{"type":"person","id":"` + linkPersonID + `"}},"volume":{"data":{"type":"volume","id":"` + linkVolumeID + `"}}}}]}`
)

func emptyContributionsJSON() string { return `{"data":[]}` }

// contributionsJSON builds a list doc from raw contribution objects.
func contributionsJSON(items ...string) string {
	return `{"data":[` + strings.Join(items, ",") + `]}`
}

func TestLinkPersonAddsCreditWithRole(t *testing.T) {
	f := newScriptedFixture(t, ok200(emptyContributionsJSON()), ok200(linkedVolumeJSON("")))
	out := runLink(t, true, "artist", "person", linkPersonID, "volume", linkVolumeID)
	if f.requests != 2 || f.methods[1] != "PATCH" {
		t.Fatalf("unexpected traffic: %v", f.methods)
	}
	if f.paths[0] != "/contributions" ||
		!strings.Contains(f.queries[0], "filter%5Bvolume_id%5D="+linkVolumeID) {
		t.Errorf("expected contributions query by volume_id, got %s?%s", f.paths[0], f.queries[0])
	}
	credits := f.bodies[1]["credits"].([]any)
	pair := credits[0].(map[string]any)
	if pair["personId"] != linkPersonID || pair["contributionType"] != "artist" {
		t.Errorf("want artist credit pair, got %v", pair)
	}
	if !strings.Contains(out, "Linked person") {
		t.Errorf("missing link confirmation:\n%s", out)
	}
}

func TestRelinkPersonAlreadyCreditedExitsZero(t *testing.T) {
	f := newScriptedFixture(t, ok200(contributionListJSON))
	out := runLink(t, true, "", "volume", linkVolumeID, "person", linkPersonID)
	if f.requests != 1 || strings.Contains(out, "Linked person") {
		t.Errorf("want contributions GET only plus notice, requests=%d out=%s", f.requests, out)
	}
	if !strings.Contains(out, "already holds the author credit") {
		t.Errorf("missing idempotency notice:\n%s", out)
	}
}

func TestUnlinkPersonDropsEveryRole(t *testing.T) {
	// One person holding author and artist roles: unlink removes both pairs.
	authorContribution := `{"type":"contribution","id":"111111111111111111111111","attributes":{"role":"author"},"relationships":{"person":{"data":{"type":"person","id":"` + linkPersonID + `"}},"volume":{"data":{"type":"volume","id":"` + linkVolumeID + `"}}}}`
	artistContribution := `{"type":"contribution","id":"222222222222222222222222","attributes":{"role":"artist"},"relationships":{"person":{"data":{"type":"person","id":"` + linkPersonID + `"}},"volume":{"data":{"type":"volume","id":"` + linkVolumeID + `"}}}}`
	f := newScriptedFixture(t, ok200(contributionsJSON(authorContribution, artistContribution)), ok200(linkedVolumeJSON("")))
	out := runLink(t, false, "", "person", linkPersonID, "volume", linkVolumeID)
	credits := f.bodies[1]["credits"].([]any)
	if len(credits) != 0 {
		t.Errorf("want all roles dropped, got %v", credits)
	}
	if !strings.Contains(out, "Unlinked person") {
		t.Errorf("missing unlink confirmation:\n%s", out)
	}
}

func TestUnlinkPersonAbsentPairExitsZero(t *testing.T) {
	f := newScriptedFixture(t, ok200(emptyContributionsJSON()))
	out := runLink(t, false, "", "person", linkPersonID, "volume", linkVolumeID)
	if f.requests != 1 || !strings.Contains(out, "isn't linked") {
		t.Errorf("want single GET plus notice, requests=%d out=%s", f.requests, out)
	}
}

func TestLinkSurfacesSubmittedDisposition(t *testing.T) {
	submitted := statusBody{http.StatusAccepted, `{"version":3,"state":"submitted","message":"needs review"}`}
	newScriptedFixture(t, ok200(linkedVolumeJSON("")), submitted)
	out := runLink(t, true, "", "volume", linkVolumeID, "publisher", linkPubID)
	if !strings.Contains(out, "Submitted proposed change: needs review") {
		t.Errorf("want submitted disposition surfaced:\n%s", out)
	}
}
