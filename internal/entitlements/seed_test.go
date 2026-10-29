package entitlements

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

var tiny = &Doc{
	Keys:        []Key{{Key: "a.b", Type: "flag"}},
	DefaultTier: "free",
	Tiers:       []Tier{{ID: "free", Name: "Free", Values: map[string]any{"a.b": false}}},
	Products: []Product{
		{ID: "p", Name: "P", Billing: "one_off", Entitlements: map[string]any{"a.b": true}},
		{ID: "q", Name: "Q", Billing: "recurring", Entitlements: map[string]any{"a.b": true}},
	},
}

// fake is an in-memory entitlements admin API. Anything in rate429 is answered with 429 first.
type fake struct {
	mu       sync.Mutex
	keys     []string
	tiers    []string
	products []map[string]any
	defTier  string
	requests []string
	bodies   map[string]string
	auth     string
	rate429  int // number of upcoming requests to answer with 429
	header   http.Header
	postCode int // status for POSTs; 0 means 201
	pageFull bool
	srv      *httptest.Server
}

func newFake(t *testing.T) *fake {
	t.Helper()
	f := &fake{bodies: map[string]string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auth = r.Header.Get("Authorization")
	raw, _ := io.ReadAll(r.Body)
	key := r.Method + " " + r.URL.Path
	f.requests = append(f.requests, key)
	f.bodies[key] = string(raw)
	if f.rate429 != 0 {
		if f.rate429 > 0 {
			f.rate429--
		}
		for k, v := range f.header {
			w.Header()[k] = v
		}
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":"rate_limited","message":"Limit exceeded"}`)
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/admin/keys":
		f.list(w, r, len(f.keys), func(i int) any { return map[string]any{"key": f.keys[i]} })
	case r.Method == http.MethodGet && r.URL.Path == "/admin/tiers":
		f.list(w, r, len(f.tiers), func(i int) any { return map[string]any{"id": f.tiers[i]} })
	case r.Method == http.MethodGet && r.URL.Path == "/admin/products":
		f.list(w, r, len(f.products), func(i int) any { return f.products[i] })
	case r.Method == http.MethodGet && r.URL.Path == "/admin/default-tier":
		_ = json.NewEncoder(w).Encode(map[string]string{"id": f.defTier})
	case r.Method == http.MethodPut && r.URL.Path == "/admin/default-tier":
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPut:
		w.WriteHeader(http.StatusOK)
	default:
		status := f.postCode
		if status == 0 {
			status = http.StatusCreated
		}
		w.WriteHeader(status)
		if status >= 400 {
			_, _ = io.WriteString(w, `{"error":"invalid_request","message":"bad"}`)
		}
	}
}

func (f *fake) list(w http.ResponseWriter, r *http.Request, n int, item func(int) any) {
	if f.pageFull && r.URL.Query().Get("skip") == "0" {
		page := make([]any, listPage)
		for i := range page {
			page[i] = map[string]any{"key": fmt.Sprint("filler", i), "id": fmt.Sprint("filler", i)}
		}
		_ = json.NewEncoder(w).Encode(page)
		return
	}
	out := make([]any, 0, n)
	if !f.pageFull {
		for i := 0; i < n; i++ {
			out = append(out, item(i))
		}
	}
	_ = json.NewEncoder(w).Encode(out)
}

func (f *fake) count(prefix string) int {
	n := 0
	for _, r := range f.requests {
		if strings.HasPrefix(r, prefix) {
			n++
		}
	}
	return n
}

func (f *fake) writes() []string {
	var out []string
	for _, r := range f.requests {
		if !strings.HasPrefix(r, "GET") {
			out = append(out, r)
		}
	}
	return out
}

// sleeps records waits instead of sleeping.
type sleeps struct{ waits []time.Duration }

func (s *sleeps) sleep(_ context.Context, d time.Duration) error {
	s.waits = append(s.waits, d)
	return nil
}

func client(f *fake, s *sleeps, logs *[]string) *Client {
	c := &Client{BaseURL: f.srv.URL + "/", Token: "tok123", Sleep: s.sleep}
	if logs != nil {
		c.Logf = func(format string, args ...any) { *logs = append(*logs, fmt.Sprintf(format, args...)) }
	}
	return c
}

func TestFreshLoadReadsOnceThenCreatesInOrderWithOverlayPrices(t *testing.T) {
	f := newFake(t)
	prices := &Prices{Products: map[string]string{"p": "members_10"}}
	rep, err := client(f, &sleeps{}, nil).Load(context.Background(), tiny, prices, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Created) != 5 || len(rep.Existing) != 0 {
		t.Errorf("report = %+v", rep)
	}
	if f.auth != "Bearer tok123" {
		t.Errorf("auth = %q", f.auth)
	}
	want := "GET /admin/keys,GET /admin/tiers,GET /admin/products,GET /admin/default-tier," +
		"POST /admin/keys,POST /admin/tiers,PUT /admin/default-tier,POST /admin/products,POST /admin/products"
	if got := strings.Join(f.requests, ","); got != want {
		t.Errorf("requests = %s", got)
	}
	if body := f.bodies["POST /admin/products"]; !strings.Contains(body, `"id":"q"`) || strings.Contains(body, "stripe_price_id") {
		t.Errorf("last product body = %s", body)
	}
}

func TestProductCreatedWithItsOverlayPriceInOneRequest(t *testing.T) {
	f := newFake(t)
	var bodies []string
	f.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/admin/products" {
			b, _ := io.ReadAll(r.Body)
			bodies = append(bodies, string(b))
			w.WriteHeader(http.StatusCreated)
			return
		}
		f.serve(w, r)
	})
	prices := &Prices{Products: map[string]string{"p": "members_10"}}
	if _, err := client(f, &sleeps{}, nil).Load(context.Background(), tiny, prices, false); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 || !strings.Contains(bodies[0], `"stripe_price_id":"members_10"`) || strings.Contains(bodies[1], "stripe_price_id") {
		t.Errorf("bodies = %v", bodies)
	}
	if f.count("PUT /admin/products") != 0 {
		t.Error("a price was set with a second request")
	}
}

func TestRerunOnAFullyLoadedServiceOnlyReads(t *testing.T) {
	f := newFake(t)
	f.keys, f.tiers, f.defTier = []string{"a.b"}, []string{"free"}, "free"
	f.products = []map[string]any{
		{"id": "p", "name": "P", "billing": "one_off", "stripe_price_id": "members_10", "entitlements": map[string]any{"a.b": true}},
		{"id": "q", "name": "Q", "billing": "recurring", "entitlements": map[string]any{"a.b": true}},
	}
	rep, err := client(f, &sleeps{}, nil).Load(context.Background(), tiny, &Prices{Products: map[string]string{"p": "members_10"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Created) != 0 || len(f.writes()) != 0 {
		t.Errorf("created %v, writes %v", rep.Created, f.writes())
	}
	if len(f.requests) != 4 {
		t.Errorf("requests = %d, want 4 reads", len(f.requests))
	}
}

func TestExistingProductGetsAPriceOnlyWhereItHasNone(t *testing.T) {
	f := newFake(t)
	f.keys, f.tiers, f.defTier = []string{"a.b"}, []string{"free"}, "free"
	f.products = []map[string]any{
		{"id": "p", "name": "P", "billing": "one_off", "entitlements": map[string]any{"a.b": map[string]any{"value": 10, "mode": "at_least"}}},
		{"id": "q", "name": "Q", "billing": "recurring", "stripe_price_id": "price_old", "entitlements": map[string]any{"a.b": true}},
	}
	prices := &Prices{Products: map[string]string{"p": "price_new", "q": "price_other"}}
	rep, err := client(f, &sleeps{}, nil).Load(context.Background(), tiny, prices, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.writes(); len(got) != 1 || got[0] != "PUT /admin/products/p" {
		t.Fatalf("writes = %v", got)
	}
	if body := f.bodies["PUT /admin/products/p"]; !strings.Contains(body, `"stripe_price_id":"price_new"`) || !strings.Contains(body, `"mode":"at_least"`) {
		t.Errorf("PUT body = %s", body)
	}
	if len(rep.Created) != 1 || rep.Created[0] != "price p" || len(rep.Skipped) != 1 || !strings.HasPrefix(rep.Skipped[0], "price q") {
		t.Errorf("report = %+v", rep)
	}
}

func TestAlreadySetDefaultTierIsKept(t *testing.T) {
	f := newFake(t)
	f.defTier = "paid"
	if _, err := client(f, &sleeps{}, nil).Load(context.Background(), tiny, nil, false); err != nil {
		t.Fatal(err)
	}
	if f.count("PUT /admin/default-tier") != 0 {
		t.Error("the default tier was changed")
	}
}

func TestDryRunReadsAndWritesNothing(t *testing.T) {
	f := newFake(t)
	rep, err := client(f, &sleeps{}, nil).Load(context.Background(), tiny, &Prices{Products: map[string]string{"p": "x"}}, true)
	if err != nil || len(rep.Created) != 5 {
		t.Fatalf("report = %+v err = %v", rep, err)
	}
	if len(f.writes()) != 0 {
		t.Errorf("dry run wrote: %v", f.writes())
	}
}

func TestFullListPagesOn(t *testing.T) {
	f := newFake(t)
	f.pageFull = true
	if _, err := client(f, &sleeps{}, nil).Load(context.Background(), tiny, nil, true); err != nil {
		t.Fatal(err)
	}
	if f.count("GET /admin/keys") != 2 {
		t.Errorf("keys requests = %d, want 2 (a full page means another)", f.count("GET /admin/keys"))
	}
}

func TestOverlayNamingAnUnknownProductIsAnError(t *testing.T) {
	f := newFake(t)
	_, err := client(f, &sleeps{}, nil).Load(context.Background(), tiny, &Prices{Products: map[string]string{"ghost": "x"}}, true)
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Errorf("err = %v", err)
	}
}

func TestPacingWaitsBetweenRequestsButNotBeforeTheFirst(t *testing.T) {
	f := newFake(t)
	s := &sleeps{}
	c := client(f, s, nil)
	c.Delay = 250 * time.Millisecond
	if _, err := c.Load(context.Background(), tiny, nil, false); err != nil {
		t.Fatal(err)
	}
	if len(s.waits) != len(f.requests)-1 {
		t.Errorf("waits = %d for %d requests", len(s.waits), len(f.requests))
	}
	for _, w := range s.waits {
		if w != 250*time.Millisecond {
			t.Errorf("wait = %s", w)
		}
	}
}

func TestRateLimitedRequestIsRetriedAfterBackoffWithAProgressNote(t *testing.T) {
	f := newFake(t)
	f.rate429 = 2
	s := &sleeps{}
	var logs []string
	if _, err := client(f, s, &logs).Load(context.Background(), tiny, nil, true); err != nil {
		t.Fatal(err)
	}
	if len(s.waits) != 2 || s.waits[0] != 5*time.Second || s.waits[1] != 10*time.Second {
		t.Errorf("waits = %v, want 5s then 10s", s.waits)
	}
	if len(logs) != 2 || !strings.Contains(logs[0], "rate limited") || !strings.Contains(logs[0], "5s") || !strings.Contains(logs[0], "1 of 6") {
		t.Errorf("logs = %v", logs)
	}
	if f.count("GET /admin/keys") != 3 {
		t.Errorf("the first request was tried %d times, want 3", f.count("GET /admin/keys"))
	}
}

func TestRetryAfterSecondsIsHonored(t *testing.T) {
	f := newFake(t)
	f.rate429 = 1
	f.header = http.Header{}
	f.header.Set("Retry-After", "17")
	s := &sleeps{}
	if _, err := client(f, s, nil).Load(context.Background(), tiny, nil, true); err != nil {
		t.Fatal(err)
	}
	if len(s.waits) != 1 || s.waits[0] != 17*time.Second {
		t.Errorf("waits = %v, want 17s", s.waits)
	}
}

func TestRetryWait(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	h := func(k, v string) http.Header {
		hdr := http.Header{}
		hdr.Set(k, v)
		return hdr
	}
	cases := []struct {
		name    string
		header  http.Header
		attempt int
		want    time.Duration
	}{
		{"no headers, first", nil, 0, 5 * time.Second},
		{"no headers, second", nil, 1, 10 * time.Second},
		{"backoff is capped", nil, 9, 60 * time.Second},
		{"retry-after seconds", h("Retry-After", "30"), 0, 30 * time.Second},
		{"retry-after date", h("Retry-After", now.Add(45*time.Second).Format(http.TimeFormat)), 0, 45 * time.Second},
		{"retry-after zero waits at least a second", h("Retry-After", "0"), 0, time.Second},
		{"retry-after is capped", h("Retry-After", "86400"), 0, maxRetryAfter},
		{"x-ratelimit-reset epoch", h("X-RateLimit-Reset", fmt.Sprint(now.Add(20*time.Second).Unix())), 0, 20 * time.Second},
		{"reset in the past waits a second", h("X-RateLimit-Reset", fmt.Sprint(now.Add(-time.Minute).Unix())), 0, time.Second},
		{"unparseable retry-after falls back to backoff", h("Retry-After", "soon"), 1, 10 * time.Second},
	}
	for _, c := range cases {
		if got := retryWait(c.header, c.attempt, now); got != c.want {
			t.Errorf("%s: wait = %s, want %s", c.name, got, c.want)
		}
	}
}

func TestExhaustedRetriesFailWithAClearMessage(t *testing.T) {
	f := newFake(t)
	f.rate429 = -1
	s := &sleeps{}
	c := client(f, s, nil)
	c.MaxRetries = 3
	_, err := c.Load(context.Background(), tiny, nil, true)
	if err == nil || !strings.Contains(err.Error(), "rate limited after 3 retries") || !strings.Contains(err.Error(), "Limit exceeded") {
		t.Fatalf("err = %v", err)
	}
	if f.count("GET /admin/keys") != 4 || len(s.waits) != 3 {
		t.Errorf("tries = %d, waits = %v", f.count("GET /admin/keys"), s.waits)
	}
}

func TestCancelledWaitStopsTheLoad(t *testing.T) {
	f := newFake(t)
	f.rate429 = -1
	c := client(f, &sleeps{}, nil)
	c.Sleep = func(context.Context, time.Duration) error { return context.Canceled }
	if _, err := c.Load(context.Background(), tiny, nil, true); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
}

func TestRealSleepStopsWhenTheContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (&Client{}).sleep(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
	if err := (&Client{}).sleep(context.Background(), time.Millisecond); err != nil {
		t.Errorf("err = %v", err)
	}
}

func TestErrorsCarryTheServiceMessageAndNoStrayNilSuffix(t *testing.T) {
	f := newFake(t)
	f.postCode = http.StatusBadRequest
	_, err := client(f, &sleeps{}, nil).Load(context.Background(), tiny, nil, false)
	want := `seed key a.b: status 400: {"error":"invalid_request","message":"bad"}`
	if err == nil || err.Error() != want {
		t.Errorf("err = %v, want %s", err, want)
	}
	if strings.Contains(err.Error(), "<nil>") {
		t.Errorf("error formats a nil: %v", err)
	}
}

func TestReadFailureNamesThePathAndStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":"forbidden"}`)
	}))
	defer srv.Close()
	_, err := (&Client{BaseURL: srv.URL, Token: "t", Sleep: (&sleeps{}).sleep}).Load(context.Background(), tiny, nil, false)
	if err == nil || !strings.Contains(err.Error(), "reading /admin/keys") || !strings.Contains(err.Error(), "status 403") || strings.Contains(err.Error(), "<nil>") {
		t.Errorf("err = %v", err)
	}
}

func TestTransportFailureIsAnError(t *testing.T) {
	f := newFake(t)
	url := f.srv.URL
	f.srv.Close()
	if _, err := (&Client{BaseURL: url, Token: "t", Sleep: (&sleeps{}).sleep}).Load(context.Background(), tiny, nil, false); err == nil {
		t.Error("expected an error")
	}
}

func TestConflictOnCreateMeansAlreadyThere(t *testing.T) {
	f := newFake(t)
	f.postCode = http.StatusConflict
	rep, err := client(f, &sleeps{}, nil).Load(context.Background(), tiny, nil, false)
	if err != nil || len(rep.Existing) != 4 || len(rep.Created) != 1 {
		t.Errorf("report = %+v err = %v (the default tier is a PUT, not a POST)", rep, err)
	}
}

func TestParseRejectsUnknownFields(t *testing.T) {
	if _, err := Parse([]byte(`{"keys":[{"key":"a.b","type":"flag","typo":1}]}`)); err == nil {
		t.Error("unknown field accepted")
	}
	if _, err := Parse([]byte(`not json`)); err == nil {
		t.Error("garbage accepted")
	}
	if d, err := Parse([]byte(`{"keys":[{"key":"a.b","type":"flag"}]}`)); err != nil || len(d.Keys) != 1 {
		t.Errorf("doc = %+v err = %v", d, err)
	}
}

func TestParsePrices(t *testing.T) {
	for name, in := range map[string]string{
		"empty":         `{"products":{"a":""}}`,
		"padded":        `{"products":{"a":" price_x"}}`,
		"unknown field": `{"products":{},"extra":1}`,
		"garbage":       `nope`,
	} {
		if _, err := ParsePrices([]byte(in)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	p, err := ParsePrices([]byte(`{"products":{"a":"members_10","b":"anything-goes.123"}}`))
	if err != nil || p.Products["a"] != "members_10" {
		t.Errorf("prices = %+v err = %v (price IDs are opaque)", p, err)
	}
}
