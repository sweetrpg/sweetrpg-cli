package cmd

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/sweetrpg/catalog-cli/internal/client"
)

const searchVolumesJSON = `{"data":[
 {"type":"volume","id":"507f1f77bcf86cd799439011","attributes":{"title":"Book M: Adventures"}},
 {"type":"volume","id":"507f1f77bcf86cd799439022","attributes":{"title":"Artifacts and Oddities"}},
 {"type":"volume","id":"507f1f77bcf86cd799439033","attributes":{"title":"The BOOK M Compendium"}}
]}`

func TestSearchListsPartialCaseInsensitiveMatches(t *testing.T) {
	newCmdFixture(t, http.StatusOK, searchVolumesJSON)

	out := runSearch(t, "volume", "book m")

	for _, want := range []string{"507f1f77bcf86cd799439011", "Book M: Adventures", "507f1f77bcf86cd799439033", "2 volume match(es)"} {
		if !strings.Contains(out, want) {
			t.Errorf("search output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Artifacts and Oddities") {
		t.Errorf("non-matching record leaked into results:\n%s", out)
	}
}

func TestSearchNoMatchesPrintsNoticeAndSucceeds(t *testing.T) {
	newCmdFixture(t, http.StatusOK, searchVolumesJSON)

	out := runSearch(t, "volume", "zzz-nonexistent")

	if !strings.Contains(out, `no volume matches for "zzz-nonexistent"`) {
		t.Errorf("expected no-match notice, got:\n%s", out)
	}
}

func TestSearchUnknownTypeIsUsageError(t *testing.T) {
	resetResolveState(t)
	err := searchCommand.RunE(searchCommand, []string{"widget", "x"})
	if err == nil || exitCodeOf(t, err) != 2 {
		t.Fatalf("want usage exit 2 for unknown type, got %v", err)
	}
}

// runSearch executes the search subcommand and returns its captured output.
func runSearch(t *testing.T, entityType, query string) string {
	t.Helper()
	resetResolveState(t)
	var out bytes.Buffer
	searchCommand.SetContext(context.Background())
	searchCommand.SetOut(&out)
	if err := searchCommand.RunE(searchCommand, []string{entityType, query}); err != nil {
		t.Fatalf("search failed: %v", err)
	}
	return out.String()
}

func TestPickMatchesPrefersExactOverPartial(t *testing.T) {
	type rec struct {
		ID   string
		Name string `json:"name"`
	}
	label := func(r *rec) string { return r.Name }
	records := []*rec{
		{"a", "Book M"},
		{"b", "My Book M Diary"},
		{"c", "BOOK M"},
	}
	recIDs := func(rs []*rec) []string {
		out := make([]string, 0, len(rs))
		for _, r := range rs {
			out = append(out, r.ID)
		}
		return out
	}

	got := pickMatches(records, "name", label, "book m")
	if want := []string{"a", "c"}; !reflect.DeepEqual(recIDs(got), want) {
		t.Errorf("want exact case-insensitive hits %v, got %v", want, recIDs(got))
	}
	if got := pickMatches(records, "name", label, "diary"); !reflect.DeepEqual(recIDs(got), []string{"b"}) {
		t.Errorf("substring-only query should fall back to partials, got %v", recIDs(got))
	}
	if got := pickMatches(records, "name", label, "nothing"); got != nil {
		t.Errorf("want nil for zero matches, got %v", recIDs(got))
	}
}

func TestFetchForFindPagesUntilShortPage(t *testing.T) {
	pageSize := 500 // must match fetchForFind's page size
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		start := r.URL.Query().Get("page[start]")
		var b strings.Builder
		b.WriteString(`{"data":[`)
		n := pageSize
		switch start {
		case "":
			n = pageSize
		case fmt.Sprint(pageSize):
			n = 3
		default:
			t.Errorf("unexpected page[start]=%q", start)
		}
		for i := 0; i < n; i++ {
			if i > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, `{"type":"volume","id":"507f1f77bcf86cd7%07d","attributes":{"title":"v%d"}}`, calls*1000+i, i)
		}
		b.WriteString(`]}`)
		w.Header().Set("Content-Type", client.MediaType)
		_, _ = w.Write([]byte(b.String()))
	}))
	t.Cleanup(srv.Close)

	oldAPIURL := flagAPIURL
	flagAPIURL = srv.URL
	t.Cleanup(func() { flagAPIURL = oldAPIURL })

	c, err := buildAnonClient()
	if err != nil {
		t.Fatalf("anon client: %v", err)
	}
	records, err := fetchForFind[struct {
		ID   string
		Name string `json:"title"`
	}](context.Background(), c, client.Entities["volume"], "anything")
	if err != nil {
		t.Fatalf("fetchForFind: %v", err)
	}
	if len(records) != pageSize+3 {
		t.Errorf("want records from both pages (%d+3), got %d", pageSize, len(records))
	}
	if calls != 2 {
		t.Errorf("want exactly 2 page requests, got %d", calls)
	}
}
