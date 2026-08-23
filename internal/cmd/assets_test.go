package cmd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sweetrpg/catalog-cli/internal/client"
)

const coverVolumeID = "aaaaaaaaaaaaaaaaaaaaaaaa"

const patchedVolumeJSON = `{"data":{"type":"volume","id":"` + coverVolumeID + `","attributes":{"title":"Dungeon World"}}}`

func writeTempImage(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("fake image bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestEditVolumeCoverAloneUploadsAndLinks(t *testing.T) {
	f := newCmdFixture(t, http.StatusOK, patchedVolumeJSON)

	var assetsRequests int
	var assetsMethod, assetsPath, assetsCT string
	assetsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assetsRequests++
		assetsMethod, assetsPath = r.Method, r.URL.Path
		assetsCT = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(assetsSrv.Close)
	oldAssets := buildAssetsClient
	buildAssetsClient = func() (*client.AssetsClient, error) {
		return client.NewAssetsClient(assetsSrv.URL, func(context.Context) (string, error) { return "test-token", nil })
	}
	t.Cleanup(func() { buildAssetsClient = oldAssets })

	child := editChildren["volume"]
	if err := child.Flags().Set("cover", writeTempImage(t, "cover.png")); err != nil {
		t.Fatal(err)
	}

	out := runEntityCommand(t, child, []string{coverVolumeID})

	if assetsRequests != 1 {
		t.Fatalf("assets uploads = %d, want 1", assetsRequests)
	}
	wantPath := "/asset/cover/" + coverVolumeID
	if assetsMethod != http.MethodPost || assetsPath != wantPath {
		t.Fatalf("upload was %s %s, want POST %s", assetsMethod, assetsPath, wantPath)
	}
	if !strings.HasPrefix(assetsCT, "multipart/form-data") {
		t.Errorf("upload content type = %q, want multipart form", assetsCT)
	}
	if !strings.Contains(string(f.gotBody), `"coverAssetId":"`+coverVolumeID+`"`) {
		t.Errorf("patch body %s missing coverAssetId link", f.gotBody)
	}
	if !strings.Contains(out, "Updated live record") {
		t.Errorf("output %q missing disposition line", out)
	}
}

func TestEditVolumeCoverRejectsUnsupportedTypeBeforeAnyHTTP(t *testing.T) {
	f := newCmdFixture(t, http.StatusOK, patchedVolumeJSON)
	var assetsRequests int
	assetsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assetsRequests++
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(assetsSrv.Close)
	oldAssets := buildAssetsClient
	buildAssetsClient = func() (*client.AssetsClient, error) {
		return client.NewAssetsClient(assetsSrv.URL, func(context.Context) (string, error) { return "", nil })
	}
	t.Cleanup(func() { buildAssetsClient = oldAssets })

	child := editChildren["volume"]
	if err := child.Flags().Set("cover", writeTempImage(t, "cover.gif")); err != nil {
		t.Fatal(err)
	}

	cmdSetErr := runEntityCommandExpectError(t, child, []string{coverVolumeID})
	if !strings.Contains(cmdSetErr.Error(), "supported types are png, jpeg, webp") {
		t.Fatalf("want unsupported-type error, got %v", cmdSetErr)
	}
	if f.requests != 0 || assetsRequests != 0 {
		t.Errorf("requests hit the wire before validation: catalog=%d assets=%d", f.requests, assetsRequests)
	}
}

func TestEditVolumeCoverWithoutAssetsURLFailsClearly(t *testing.T) {
	newCmdFixture(t, http.StatusOK, patchedVolumeJSON)
	t.Setenv("SWEETRPG_CATALOG_API_URL", "http://127.0.0.1:9")
	t.Setenv("SWEETRPG_AUTH_DOMAIN", "dev.example.auth0.com")
	t.Setenv("SWEETRPG_AUTH_CLIENT_ID", "client")
	t.Setenv("SWEETRPG_AUTH_AUDIENCE", "https://catalog-api")
	t.Setenv("SWEETRPG_ASSETS_WEB_URL", "")
	oldAssets := buildAssetsClient
	buildAssetsClient = newAssetsClient
	t.Cleanup(func() { buildAssetsClient = oldAssets })

	child := editChildren["volume"]
	if err := child.Flags().Set("cover", writeTempImage(t, "cover.png")); err != nil {
		t.Fatal(err)
	}

	err := runEntityCommandExpectError(t, child, []string{coverVolumeID})
	if !strings.Contains(err.Error(), "invalid assets web base URL") {
		t.Fatalf("want assets URL configuration error, got %v", err)
	}
}
