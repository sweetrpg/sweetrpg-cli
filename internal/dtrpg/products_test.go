package dtrpg

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pilgrimagesoftware/dtrpg-sdk.go/library"
)

func testSession(baseURL string) *Session {
	return &Session{lib: library.NewClient(library.NewConfigWithBaseURL("k", baseURL), "tok")}
}

const pageOne = `{"links":{"self":"?page=1","next":"?page=2"},"meta":{"itemsPerPage":1,"currentPage":1},
"data":[{"id":"1","type":"OrderProduct","attributes":{"orderId":1,"productId":11,"royaltyPublisherId":0,"name":"Book One","finalPrice":0,"quantity":1,"bundleId":0,"archived":0,"orderProductId":101,"customerId":0,"files":[],"filters":[],"history":[],"attributes":[]}}]}`

const pageTwo = `{"links":{"self":"?page=2","next":null},"meta":{"itemsPerPage":1,"currentPage":2},
"data":[{"id":"2","type":"OrderProduct","attributes":{"orderId":2,"productId":22,"royaltyPublisherId":0,"name":"Book Two","finalPrice":0,"quantity":1,"bundleId":0,"archived":1,"orderProductId":102,"customerId":0,"files":[],"filters":[],"history":[],"attributes":[]}}]}`

func TestFetchLibraryAggregatesAllPages(t *testing.T) {
	var pages []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		pages = append(pages, page)
		w.Header().Set("Content-Type", "application/json")
		if page == "2" {
			_, _ = w.Write([]byte(pageTwo))
			return
		}
		_, _ = w.Write([]byte(pageOne))
	}))
	defer srv.Close()

	var onPageCalls [][2]int
	lib, err := testSession(srv.URL).FetchLibrary(context.Background(), 1, func(page, fetched int) {
		onPageCalls = append(onPageCalls, [2]int{page, fetched})
	})
	if err != nil {
		t.Fatalf("FetchLibrary: %v", err)
	}
	// Item 2 is archived; both must come back from one unfiltered pass - see
	// TestFetchLibraryNeverSetsArchivedFilter for why the archived param is
	// never sent at all.
	if len(lib.Products) != 2 {
		t.Fatalf("got %d products, want 2 (both active and archived items)", len(lib.Products))
	}
	if len(pages) != 2 || pages[0] != "1" || pages[1] != "2" {
		t.Fatalf("requested pages = %v, want [1 2]", pages)
	}
	if want := [][2]int{{1, 1}, {2, 2}}; len(onPageCalls) != 2 || onPageCalls[0] != want[0] || onPageCalls[1] != want[1] {
		t.Fatalf("onPage calls = %v, want %v", onPageCalls, want)
	}
}

// TestFetchLibraryNeverSetsArchivedFilter guards the bug in #24: passing
// archived=true unconditionally returned only the account's archived subset
// (40 of ~1700 real titles), not the full library. FetchLibrary must not
// filter by archived state at all - each item's own Archived field drives
// classification downstream instead.
func TestFetchLibraryNeverSetsArchivedFilter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("archived"); got != "" {
			t.Errorf("request set archived=%q, want the param omitted entirely", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(pageTwo))
	}))
	defer srv.Close()

	if _, err := testSession(srv.URL).FetchLibrary(context.Background(), 0, nil); err != nil {
		t.Fatalf("FetchLibrary: %v", err)
	}
}

func TestFetchLibraryHonorsRetryAfter(t *testing.T) {
	var slept []time.Duration
	orig := sleepFn
	sleepFn = func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }
	defer func() { sleepFn = orig }()

	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"message":"slow down"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(pageTwo))
	}))
	defer srv.Close()

	lib, err := testSession(srv.URL).FetchLibrary(context.Background(), 0, nil)
	if err != nil {
		t.Fatalf("FetchLibrary: %v", err)
	}
	if len(lib.Products) != 1 {
		t.Fatalf("got %d products, want 1", len(lib.Products))
	}
	if len(slept) != 1 || slept[0] != 7*time.Second {
		t.Fatalf("slept = %v, want [7s]", slept)
	}
}
