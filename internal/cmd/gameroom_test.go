package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/sweetrpg/sweetrpg-cli/internal/auth"
	"github.com/sweetrpg/sweetrpg-cli/internal/client"
	"github.com/sweetrpg/sweetrpg-cli/internal/dtrpg"
)

func runGameRoomImportChild(t *testing.T, name string, args ...string) (string, error) {
	t.Helper()
	buildTree()
	child := gameRoomImportChildren[name]
	if child == nil {
		t.Fatalf("game-room import child %q missing", name)
	}
	var out bytes.Buffer
	child.SetContext(context.Background())
	child.SetOut(&out)
	err := child.RunE(child, args)
	return out.String(), err
}

// gameRoomStub records POSTs to /library/entries and serves a scripted
// current library for GET /library.
type gameRoomStub struct {
	mu             sync.Mutex
	existingVolIDs []string
	posts          []struct{ volumeID, volumeTitle string }
}

func (s *gameRoomStub) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/library"):
			entries := make([]string, 0, len(s.existingVolIDs))
			for _, id := range s.existingVolIDs {
				entries = append(entries, fmt.Sprintf(`{"volume_id":%q}`, id))
			}
			_, _ = io.WriteString(w, fmt.Sprintf(`{"entries":[%s]}`, strings.Join(entries, ",")))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/library/entries"):
			var body struct {
				VolumeID    string `json:"volume_id"`
				VolumeTitle string `json:"volume_title"`
			}
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &body)
			s.posts = append(s.posts, struct{ volumeID, volumeTitle string }{body.VolumeID, body.VolumeTitle})
			_, _ = io.WriteString(w, `{}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

// catalogVolumesStub serves a fixed GET /volumes list for the game-room
// import's catalog-matching scan.
func catalogVolumesStub(t *testing.T, volumesJSON string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", client.MediaType)
		if r.Method == http.MethodGet && r.URL.Path == "/volumes" {
			_, _ = io.WriteString(w, volumesJSON)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv
}

const gameRoomVolumesJSON = `{"data":[
	{"type":"volume","id":"vol-matched","attributes":{"title":"Book One","properties":[{"name":"dtrpg_product_id","kind":"string","value":"1"}]}},
	{"type":"volume","id":"vol-present","attributes":{"title":"Book Two","properties":[{"name":"dtrpg_product_id","kind":"string","value":"2"}]}}
]}`

// setupGameRoomImport wires the catalog and game-room-api fixture servers,
// a scripted DTRPG library, and the game-room import's own key store.
func setupGameRoomImport(t *testing.T, gr *gameRoomStub, catalogJSON string, dtrpgSrv *httptest.Server) {
	t.Helper()
	catSrv := catalogVolumesStub(t, catalogJSON)
	grSrv := httptest.NewServer(gr.handler())
	t.Cleanup(grSrv.Close)

	oldAnon, oldReq := buildAnonClient, requirePlatformSession
	buildAnonClient = func() (*client.Client, error) {
		return client.New(catSrv.URL, func(context.Context) (string, error) { return "tok", nil })
	}
	requirePlatformSession = func() error { return nil }
	t.Cleanup(func() { buildAnonClient, requirePlatformSession = oldAnon, oldReq })

	oldSessionLoad := platformSessionLoad
	platformSessionLoad = func() (*auth.Session, error) { return &auth.Session{Account: "user-123"}, nil }
	t.Cleanup(func() { platformSessionLoad = oldSessionLoad })

	oldResolve := resolveAPIRequest
	resolveAPIRequest = func(service string) (string, func(context.Context) (string, error), error) {
		return grSrv.URL, func(context.Context) (string, error) { return "gr-token", nil }, nil
	}
	t.Cleanup(func() { resolveAPIRequest = oldResolve })

	store := &dtrpg.MemoryKeyStore{}
	_ = store.SaveKey("app-key")
	oldStore := dtrpgKeyStore
	dtrpgKeyStore = func() dtrpg.KeyStore { return store }
	t.Cleanup(func() { dtrpgKeyStore = oldStore })

	oldBase := dtrpgLoginBase
	dtrpgLoginBase = dtrpgSrv.URL
	t.Cleanup(func() { dtrpgLoginBase = oldBase })

	oldDry := flagGameRoomImportDryRun
	flagGameRoomImportDryRun = false
	t.Cleanup(func() { flagGameRoomImportDryRun = oldDry })
}

func TestGameRoomImportMatchesAndSkipsAndReportsAlreadyPresent(t *testing.T) {
	srv := dtrpgLibraryServer(t,
		dtrpgProductJSON(1, "Book One", "Pub A", 0),
		dtrpgProductJSON(2, "Book Two", "Pub B", 0),
		dtrpgProductJSON(3, "Book Three (no catalog match)", "Pub C", 0),
	)
	gr := &gameRoomStub{existingVolIDs: []string{"vol-present"}}
	setupGameRoomImport(t, gr, gameRoomVolumesJSON, srv)

	out, err := runGameRoomImportChild(t, "dtrpg")
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(gr.posts) != 1 || gr.posts[0].volumeID != "vol-matched" || gr.posts[0].volumeTitle != "Book One" {
		t.Fatalf("posts = %+v, want one entry for vol-matched", gr.posts)
	}
	if !strings.Contains(out, "1 added") || !strings.Contains(out, "1 already present") ||
		!strings.Contains(out, "1 skipped (not in catalog)") {
		t.Errorf("summary missing expected counts:\n%s", out)
	}
	if !strings.Contains(out, "Book Three (no catalog match)") {
		t.Errorf("summary should list the skipped product's title:\n%s", out)
	}
}

func TestGameRoomImportDryRunMakesNoWrites(t *testing.T) {
	srv := dtrpgLibraryServer(t, dtrpgProductJSON(1, "Book One", "Pub A", 0))
	gr := &gameRoomStub{}
	setupGameRoomImport(t, gr, gameRoomVolumesJSON, srv)
	flagGameRoomImportDryRun = true

	out, err := runGameRoomImportChild(t, "dtrpg")
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if len(gr.posts) != 0 {
		t.Fatalf("dry-run must not write, got %d posts", len(gr.posts))
	}
	if !strings.Contains(out, "1 added") || !strings.Contains(out, "dry run") {
		t.Errorf("dry-run summary missing expectations:\n%s", out)
	}
}
