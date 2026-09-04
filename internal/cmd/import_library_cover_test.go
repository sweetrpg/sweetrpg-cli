package cmd

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sweetrpg/catalog-cli/internal/client"
	"github.com/sweetrpg/catalog-cli/internal/dtrpg"
)

func TestDTRPGLibraryAttachesCoversAndIsolatesFailures(t *testing.T) {
	stub := &catalogStub{}
	srv := dtrpgLibraryServer(t,
		dtrpgProductJSON(1, "Has Cover", "", 0),
		dtrpgProductJSON(2, "Bad Cover", "", 0))
	setupImportLibrary(t, stub, srv, true)

	var uploads int
	assetsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uploads++
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(assetsSrv.Close)
	oldAssets := buildAssetsClient
	buildAssetsClient = func() (*client.AssetsClient, error) {
		return client.NewAssetsClient(assetsSrv.URL, func(context.Context) (string, error) { return "tok", nil })
	}
	t.Cleanup(func() { buildAssetsClient = oldAssets })

	oldFetch := fetchCover
	fetchCover = func(_ context.Context, url string) (*dtrpg.Cover, error) {
		if strings.Contains(url, "1.jpg") {
			return &dtrpg.Cover{Data: []byte("png-bytes"), ContentType: "image/png"}, nil
		}
		return nil, errors.New("cover fetch failed: 404")
	}
	t.Cleanup(func() { fetchCover = oldFetch })

	out, err := runImportChild(t, "library")
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if stub.volumePOSTs != 2 {
		t.Fatalf("volumes created = %d, want 2 (cover failure must not fail the product)", stub.volumePOSTs)
	}
	if uploads != 1 {
		t.Fatalf("cover uploads = %d, want 1", uploads)
	}
	if !strings.Contains(out, "1 attached, 1 skipped") {
		t.Errorf("summary missing cover counts:\n%s", out)
	}
	if !strings.Contains(out, "cover skipped for Bad Cover") {
		t.Errorf("summary missing skipped-cover detail:\n%s", out)
	}
}

func TestDTRPGLibraryCoversDisabledWithoutAssetsConfig(t *testing.T) {
	stub := &catalogStub{}
	srv := dtrpgLibraryServer(t, dtrpgProductJSON(1, "Book", "", 0))
	setupImportLibrary(t, stub, srv, true)

	oldAssets := buildAssetsClient
	buildAssetsClient = func() (*client.AssetsClient, error) {
		return nil, errors.New("invalid assets web base URL \"\"")
	}
	t.Cleanup(func() { buildAssetsClient = oldAssets })

	out, err := runImportChild(t, "library")
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if stub.volumePOSTs != 1 {
		t.Fatalf("volumes created = %d, want 1 (missing assets config must not block the import)", stub.volumePOSTs)
	}
	if !strings.Contains(out, "covers disabled") || !strings.Contains(out, "0 attached, 1 skipped") {
		t.Errorf("summary missing disabled-covers reporting:\n%s", out)
	}
}
