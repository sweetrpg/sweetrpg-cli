package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// assetFixture is a scripted assets-web: it records the last upload and
// replies 201 like the real store_asset handler.
type assetFixture struct {
	t           *testing.T
	gotMethod   string
	gotPath     string
	gotAuth     string
	gotBoundary string
	partName    string
	partFile    string
	partCT      string
	partBody    []byte
}

func newAssetFixture(t *testing.T) (*assetFixture, *AssetsClient) {
	t.Helper()
	f := &assetFixture{t: t}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.gotMethod, f.gotPath, f.gotAuth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err == nil && strings.HasPrefix(mediaType, "multipart/") {
			f.gotBoundary = params["boundary"]
			reader := multipart.NewReader(r.Body, f.gotBoundary)
			form, err := reader.ReadForm(1 << 20)
			if err != nil {
				t.Errorf("parsing multipart body: %v", err)
				http.Error(w, "bad form", http.StatusBadRequest)
				return
			}
			if len(form.File["file"]) == 0 {
				t.Error("no file part")
				http.Error(w, "no file", http.StatusBadRequest)
				return
			}
			header := form.File["file"][0]
			file, err := header.Open()
			if err != nil {
				t.Errorf("opening file part: %v", err)
				http.Error(w, "unreadable", http.StatusBadRequest)
				return
			}
			defer file.Close()
			f.partName = "file"
			f.partFile = header.Filename
			f.partCT = header.Header.Get("Content-Type")
			f.partBody, _ = io.ReadAll(file)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"kind":"cover","id":"abc123"}`)
	}))
	t.Cleanup(srv.Close)
	ac, err := NewAssetsClient(srv.URL, func(context.Context) (string, error) { return "test-token", nil })
	if err != nil {
		t.Fatal(err)
	}
	return f, ac
}

func TestAssetsUploadSendsMultipartFile(t *testing.T) {
	f, ac := newAssetFixture(t)

	err := ac.Upload(context.Background(), AssetKindCover, "abc123", []byte("pngbytes"), "image/png")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if f.gotMethod != http.MethodPost || f.gotPath != "/asset/cover/abc123" {
		t.Errorf("request was %s %s", f.gotMethod, f.gotPath)
	}
	if f.gotAuth != "Bearer test-token" {
		t.Errorf("missing bearer auth, got %q", f.gotAuth)
	}
	if f.partName != "file" || f.partFile != "abc123" {
		t.Errorf("part name/file = %q/%q", f.partName, f.partFile)
	}
	if f.partCT != "image/png" {
		t.Errorf("part content type = %q", f.partCT)
	}
	if string(f.partBody) != "pngbytes" {
		t.Errorf("part body = %q", f.partBody)
	}
}

func TestAssetsUploadMapsErrorMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"message":"Unsupported content type: image/gif"}`)
	}))
	t.Cleanup(srv.Close)
	ac, err := NewAssetsClient(srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}

	err = ac.Upload(context.Background(), AssetKindSample, "abc123-0", []byte("x"), "image/gif")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400 APIError, got %v", err)
	}
	if apiErr.Message != "Unsupported content type: image/gif" {
		t.Errorf("message = %q", apiErr.Message)
	}
	if apiErr.Service != "assets-web" || !strings.Contains(apiErr.Error(), "assets-web") {
		t.Errorf("error should name assets-web, got %q", apiErr.Error())
	}
}

func TestNewAssetsClientRejectsInvalidURL(t *testing.T) {
	for _, bad := range []string{"", "not a url", "ftp://x.example.com"} {
		if _, err := NewAssetsClient(bad, nil); err == nil {
			t.Errorf("NewAssetsClient(%q) should fail", bad)
		}
	}
}

func TestContentTypeForFilename(t *testing.T) {
	cases := map[string]string{
		"dw.png": "image/png",
		"DW.PNG": "image/png",
		"a.JPG":  "image/jpeg",
		"b.jpeg": "image/jpeg",
		"c.webp": "image/webp",
		"d.gif":  "",
		"noext":  "",
	}
	for name, want := range cases {
		got, err := ContentTypeForFilename(name)
		if want == "" {
			if err == nil {
				t.Errorf("ContentTypeForFilename(%q) should fail", name)
			}
			continue
		}
		if err != nil || got != want {
			t.Errorf("ContentTypeForFilename(%q) = %q, %v; want %q", name, got, err, want)
		}
	}
}

// twoServiceFixture wires one catalog-api fixture and one assets-web fixture so
// tests can assert the full upload-then-link sequence across both services.
type twoServiceFixture struct {
	api    *fixtureServer
	client *Client
	assets *AssetsClient
}

func newTwoServices(t *testing.T, apiStatus int, apiBody string) *twoServiceFixture {
	t.Helper()
	f, c := newFixture(t, apiStatus, apiBody)
	_, ac := newAssetFixture(t)
	return &twoServiceFixture{api: f, client: c, assets: ac}
}

func TestSetVolumeCoverUploadsThenLinks(t *testing.T) {
	ts := newTwoServices(t, http.StatusOK, oneVolumeJSON)

	vol, disp, err := SetVolumeCover(context.Background(), ts.client, ts.assets, "abc123", []byte("pngbytes"), "image/png")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if disp.Submitted {
		t.Fatalf("editor write should be live, got %+v", disp)
	}
	if vol.ID != "abc123" {
		t.Errorf("decoded id = %q", vol.ID)
	}

	if ts.api.gotMethod != http.MethodPatch || ts.api.gotPath != "/volumes/abc123" {
		t.Errorf("catalog request was %s %s (upload must come first)", ts.api.gotMethod, ts.api.gotPath)
	}
	var fields map[string]any
	if err := json.Unmarshal(ts.api.gotBody, &fields); err != nil {
		t.Fatalf("patch body: %v", err)
	}
	if fields["coverAssetId"] != "abc123" {
		t.Errorf("patch body = %s", ts.api.gotBody)
	}
}

func TestSetVolumeSamplesUploadsInOrderThenLinks(t *testing.T) {
	ts := newTwoServices(t, http.StatusAccepted, `{"version":4,"state":"submitted","message":"Change submitted for review"}`)
	uploads := make(chan string, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uploads <- r.URL.Path
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{}`)
	}))
	t.Cleanup(srv.Close)
	assets, err := NewAssetsClient(srv.URL, func(context.Context) (string, error) { return "tok", nil })
	if err != nil {
		t.Fatal(err)
	}

	vol, disp, err := SetVolumeSamples(context.Background(), ts.client, assets, "abc123",
		[][]byte{[]byte("one"), []byte("two")}, "image/webp")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	close(uploads)
	var got []string
	for path := range uploads {
		got = append(got, path)
	}
	want := []string{"/asset/sample/abc123-0", "/asset/sample/abc123-1"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("uploads = %v, want %v in order", got, want)
	}

	if !disp.Submitted || disp.Version != 4 || disp.State != "submitted" {
		t.Errorf("disposition = %+v", disp)
	}
	if vol != nil {
		t.Errorf("submitted write should not decode a live record")
	}
	var fields map[string]any
	if err := json.Unmarshal(ts.api.gotBody, &fields); err != nil {
		t.Fatal(err)
	}
	ids, _ := fields["sampleAssetIds"].([]any)
	if len(ids) != 2 || ids[0] != "abc123-0" || ids[1] != "abc123-1" {
		t.Errorf("patch body = %s", ts.api.gotBody)
	}
}

func TestSetVolumeSamplesEnforcesCapBeforeAnyCall(t *testing.T) {
	ts := newTwoServices(t, http.StatusOK, oneVolumeJSON)

	tooMany := make([][]byte, maxVolumeSamples+1)
	_, _, err := SetVolumeSamples(context.Background(), ts.client, ts.assets, "abc123", tooMany, "image/png")
	if err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("want cap error before any request, got %v", err)
	}
	if ts.api.gotMethod != "" {
		t.Errorf("no API call expected, saw %s %s", ts.api.gotMethod, ts.api.gotPath)
	}
}

func TestSetVolumeSamplesEmptyClearsList(t *testing.T) {
	ts := newTwoServices(t, http.StatusOK, oneVolumeJSON)

	_, _, err := SetVolumeSamples(context.Background(), ts.client, ts.assets, "abc123", nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(ts.api.gotBody, &fields); err != nil {
		t.Fatal(err)
	}
	ids, ok := fields["sampleAssetIds"].([]any)
	if !ok || len(ids) != 0 {
		t.Errorf("patch body = %s, want empty sampleAssetIds array", ts.api.gotBody)
	}
}

func TestSetVolumeCoverFailsWithoutLinkingWhenUploadFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"too large"}`, http.StatusRequestEntityTooLarge)
	}))
	t.Cleanup(srv.Close)
	assets, err := NewAssetsClient(srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := newTwoServices(t, http.StatusOK, oneVolumeJSON)

	_, _, err = SetVolumeCover(context.Background(), ts.client, assets, "abc123", []byte("big"), "image/png")
	if err == nil || !strings.Contains(err.Error(), "uploading cover") {
		t.Fatalf("want upload error wrapping assets-web failure, got %v", err)
	}
	if ts.api.gotMethod != "" {
		t.Errorf("PATCH must not fire when upload fails, saw %s", ts.api.gotMethod)
	}
}

func TestAssetsUploadTokenFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("request should not be sent when token source fails")
	}))
	t.Cleanup(srv.Close)
	ac, err := NewAssetsClient(srv.URL, func(context.Context) (string, error) { return "", errors.New("keychain locked") })
	if err != nil {
		t.Fatal(err)
	}

	err = ac.Upload(context.Background(), AssetKindCover, "abc123", []byte("x"), "image/png")
	if err == nil || !strings.Contains(err.Error(), "keychain locked") {
		t.Fatalf("want token error surfaced, got %v", err)
	}
}
