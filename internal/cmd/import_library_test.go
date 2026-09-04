package cmd

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/sweetrpg/catalog-cli/internal/client"
	"github.com/sweetrpg/catalog-cli/internal/dtrpg"
)

// catalogStub routes the handful of catalog-api calls the import makes and
// records what was written.
type catalogStub struct {
	mu             sync.Mutex
	volumesJSON    string
	publishersJSON string
	failVolumeName string
	volumePOSTs    int
	publisherPOSTs int
	patches        int
	nextID         int
	linkedPubIDs   []string
}

func (s *catalogStub) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", client.MediaType)

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/volumes":
			_, _ = io.WriteString(w, orEmptyList(s.volumesJSON))
		case r.Method == http.MethodGet && r.URL.Path == "/publishers":
			_, _ = io.WriteString(w, orEmptyList(s.publishersJSON))
		case r.Method == http.MethodPost && r.URL.Path == "/publishers":
			s.publisherPOSTs++
			s.nextID++
			_, _ = io.WriteString(w, fmt.Sprintf(
				`{"data":{"type":"publisher","id":"pub-%d","attributes":{}}}`, s.nextID))
		case r.Method == http.MethodPost && r.URL.Path == "/volumes":
			s.volumePOSTs++
			if s.failVolumeName != "" && strings.Contains(string(body), `"title":"`+s.failVolumeName+`"`) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = io.WriteString(w, `{"message":"volume rejected"}`)
				return
			}
			s.nextID++
			_, _ = io.WriteString(w, fmt.Sprintf(
				`{"data":{"type":"volume","id":"vol-%d","attributes":{}}}`, s.nextID))
		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/volumes/"):
			s.patches++
			if i := strings.Index(string(body), `"publisherIds":["`); i >= 0 {
				rest := string(body)[i+len(`"publisherIds":["`):]
				if j := strings.Index(rest, `"`); j >= 0 {
					s.linkedPubIDs = append(s.linkedPubIDs, rest[:j])
				}
			}
			_, _ = io.WriteString(w, `{"data":{"type":"volume","id":"vol-x","attributes":{}}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func orEmptyList(s string) string {
	if s == "" {
		return `{"data":[]}`
	}
	return s
}

// dtrpgLibraryServer serves auth_key plus a single-page order_products response
// built from the given product fragments.
func dtrpgLibraryServer(t *testing.T, products ...string) *httptest.Server {
	t.Helper()
	page := fmt.Sprintf(
		`{"links":{"self":"?page=1","next":null},"meta":{"itemsPerPage":50,"currentPage":1},"data":[%s]}`,
		strings.Join(products, ","))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/auth_key") {
			_, _ = io.WriteString(w, `{"token":"jwt","refreshToken":"r","refreshTokenTTL":9999999999}`)
			return
		}
		_, _ = io.WriteString(w, page)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func dtrpgProductJSON(id int, title, publisher string, archived int) string {
	return fmt.Sprintf(`{"id":"%d","type":"OrderProduct","attributes":{
		"orderId":1,"productId":%d,"royaltyPublisherId":0,"name":%q,"finalPrice":0,"quantity":1,
		"bundleId":0,"archived":%d,"orderProductId":%d,"customerId":0,
		"isbn":null,"datePurchased":"2022-02-02",
		"files":[],"filters":[{"filterId":1,"parentFilterId":0,"name":"Fantasy","parentName":""}],
		"history":[],"attributes":[],
		"publisher":{"name":%q,"publisherId":0,"slug":""},
		"product":{"productId":%d,"bundleId":0,"image":"covers/%d.jpg",
			"description":{"name":%q,"slug":"","shortDescription":"About %s"}}
	}}`, id, id, title, archived, 1000+id, publisher, id, id, title, title)
}

func setupImportLibrary(t *testing.T, stub *catalogStub, dtrpgSrv *httptest.Server, keySet bool) {
	t.Helper()
	catSrv := httptest.NewServer(stub.handler())
	t.Cleanup(catSrv.Close)

	oldBuild, oldAnon, oldReq := buildAPIClient, buildAnonClient, requirePlatformSession
	buildAPIClient = func() (*client.Client, error) {
		return client.New(catSrv.URL, func(context.Context) (string, error) { return "tok", nil })
	}
	buildAnonClient = buildAPIClient
	requirePlatformSession = func() error { return nil }
	t.Cleanup(func() {
		buildAPIClient, buildAnonClient, requirePlatformSession = oldBuild, oldAnon, oldReq
	})

	store := &dtrpg.MemoryKeyStore{}
	if keySet {
		_ = store.SaveKey("app-key")
	}
	withImportSeams(t, store, dtrpgSrv.URL)

	oldDry, oldArch, oldPage := flagImportDryRun, flagImportArchived, flagImportPageSize
	flagImportDryRun, flagImportArchived, flagImportPageSize = false, false, 0
	t.Cleanup(func() {
		flagImportDryRun, flagImportArchived, flagImportPageSize = oldDry, oldArch, oldPage
	})
}

func TestDTRPGLibraryDryRunMakesNoWrites(t *testing.T) {
	stub := &catalogStub{}
	srv := dtrpgLibraryServer(t,
		dtrpgProductJSON(1, "Book One", "Pub A", 0),
		dtrpgProductJSON(2, "Book Two", "Pub B", 0))
	setupImportLibrary(t, stub, srv, true)
	flagImportDryRun = true

	out, err := runImportChild(t, "library")
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if stub.volumePOSTs != 0 || stub.publisherPOSTs != 0 || stub.patches != 0 {
		t.Fatalf("dry-run wrote: volumes=%d publishers=%d patches=%d", stub.volumePOSTs, stub.publisherPOSTs, stub.patches)
	}
	if !strings.Contains(out, "2 to import") || !strings.Contains(out, "no records created") {
		t.Errorf("plan output missing expectations:\n%s", out)
	}
}

func TestDTRPGLibraryImportsFreshProducts(t *testing.T) {
	stub := &catalogStub{}
	srv := dtrpgLibraryServer(t,
		dtrpgProductJSON(1, "Book One", "Pub A", 0),
		dtrpgProductJSON(2, "Book Two", "Pub B", 0))
	setupImportLibrary(t, stub, srv, true)

	out, err := runImportChild(t, "library")
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if stub.volumePOSTs != 2 || stub.publisherPOSTs != 2 || stub.patches != 2 {
		t.Fatalf("writes: volumes=%d publishers=%d patches=%d, want 2/2/2", stub.volumePOSTs, stub.publisherPOSTs, stub.patches)
	}
	if !strings.Contains(out, "2 imported") {
		t.Errorf("summary missing count:\n%s", out)
	}
}

func TestDTRPGLibraryReusesExistingPublisher(t *testing.T) {
	stub := &catalogStub{
		publishersJSON: `{"data":[{"type":"publisher","id":"pub-existing","attributes":{"name":"evil hat productions"}}]}`,
	}
	srv := dtrpgLibraryServer(t,
		dtrpgProductJSON(1, "Fate Core", "Evil Hat Productions", 0),
		dtrpgProductJSON(2, "Fate Accelerated", "Evil Hat Productions", 0))
	setupImportLibrary(t, stub, srv, true)

	if _, err := runImportChild(t, "library"); err != nil {
		t.Fatalf("import: %v", err)
	}
	if stub.publisherPOSTs != 0 {
		t.Errorf("created %d publishers, want 0 (case-insensitive reuse)", stub.publisherPOSTs)
	}
	if len(stub.linkedPubIDs) != 2 || stub.linkedPubIDs[0] != "pub-existing" || stub.linkedPubIDs[1] != "pub-existing" {
		t.Errorf("linked publisher ids = %v, want both pub-existing", stub.linkedPubIDs)
	}
}

func TestDTRPGLibrarySkipsAlreadyImported(t *testing.T) {
	stub := &catalogStub{
		volumesJSON: `{"data":[{"type":"volume","id":"vol-old","attributes":{"title":"Book One","properties":[{"name":"dtrpg_product_id","kind":"string","value":"1"}]}}]}`,
	}
	srv := dtrpgLibraryServer(t, dtrpgProductJSON(1, "Book One", "Pub A", 0))
	setupImportLibrary(t, stub, srv, true)

	out, err := runImportChild(t, "library")
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if stub.volumePOSTs != 0 {
		t.Errorf("re-import created %d volumes, want 0", stub.volumePOSTs)
	}
	if !strings.Contains(out, "1 already imported") {
		t.Errorf("summary missing already-imported count:\n%s", out)
	}
}

func TestDTRPGLibraryArchivedHandling(t *testing.T) {
	products := []string{
		dtrpgProductJSON(1, "Live Book", "Pub A", 0),
		dtrpgProductJSON(2, "Shelved Book", "Pub A", 1),
	}

	stub := &catalogStub{}
	setupImportLibrary(t, stub, dtrpgLibraryServer(t, products...), true)
	out, err := runImportChild(t, "library")
	if err != nil {
		t.Fatalf("default import: %v", err)
	}
	if stub.volumePOSTs != 1 || !strings.Contains(out, "1 skipped (archived)") {
		t.Fatalf("default run: volumes=%d out=%s", stub.volumePOSTs, out)
	}

	stub2 := &catalogStub{}
	setupImportLibrary(t, stub2, dtrpgLibraryServer(t, products...), true)
	flagImportArchived = true
	if _, err := runImportChild(t, "library"); err != nil {
		t.Fatalf("include-archived import: %v", err)
	}
	if stub2.volumePOSTs != 2 {
		t.Errorf("--include-archived created %d volumes, want 2", stub2.volumePOSTs)
	}
}

func TestDTRPGLibraryFailureIsolation(t *testing.T) {
	stub := &catalogStub{failVolumeName: "Boom"}
	srv := dtrpgLibraryServer(t,
		dtrpgProductJSON(1, "Alpha", "Pub A", 0),
		dtrpgProductJSON(2, "Boom", "Pub A", 0),
		dtrpgProductJSON(3, "Gamma", "Pub A", 0))
	setupImportLibrary(t, stub, srv, true)

	out, err := runImportChild(t, "library")
	if exitCodeOf(t, err) != 1 {
		t.Fatalf("want exit 1 on partial failure, got %v", err)
	}
	if stub.volumePOSTs != 3 {
		t.Errorf("attempted %d volume creates, want 3 (run continued past failure)", stub.volumePOSTs)
	}
	if !strings.Contains(out, "2 imported") || !strings.Contains(out, "Boom") {
		t.Errorf("summary should report 2 imported and name the failure:\n%s", out)
	}
}

func TestDTRPGLibraryMissingPlatformSessionExits3(t *testing.T) {
	stub := &catalogStub{}
	srv := dtrpgLibraryServer(t, dtrpgProductJSON(1, "Book", "Pub", 0))
	setupImportLibrary(t, stub, srv, true)
	requirePlatformSession = func() error { return &ExitError{Code: 3, Err: fmt.Errorf("not logged in")} }

	_, err := runImportChild(t, "library")
	if exitCodeOf(t, err) != 3 {
		t.Fatalf("want exit 3, got %v", err)
	}
}

func TestDTRPGLibraryMissingKeyExitsNonZero(t *testing.T) {
	stub := &catalogStub{}
	srv := dtrpgLibraryServer(t, dtrpgProductJSON(1, "Book", "Pub", 0))
	setupImportLibrary(t, stub, srv, false)

	_, err := runImportChild(t, "library")
	if err == nil || exitCodeOf(t, err) == 0 {
		t.Fatalf("want non-zero exit on missing key, got %v", err)
	}
	if !strings.Contains(err.Error(), "import dtrpg login") {
		t.Errorf("error should direct to login: %v", err)
	}
}
