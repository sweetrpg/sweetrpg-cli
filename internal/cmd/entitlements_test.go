package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/sweetrpg/sweetrpg-cli/internal/auth"
)

const entSeed = `{
  "keys": [{"key": "a.b", "type": "limit", "max": 5}],
  "default_tier": "free",
  "tiers": [{"id": "free", "name": "Free", "values": {"a.b": 1}}],
  "products": [{"id": "p", "name": "P", "billing": "one_off", "entitlements": {"a.b": 1}}]
}`

const entPrices = `{"products": {"p": "members_10"}}`

// entServer stands in for entitlements-api's admin API.
type entServer struct {
	mu       sync.Mutex
	requests []string
	auth     []string
	status   int // status for POSTs; 0 means 201
	products string
	limited  int // upcoming requests to answer 429 with Retry-After: 1
	srv      *httptest.Server
}

func newEntServer(t *testing.T) *entServer {
	t.Helper()
	e := &entServer{}
	e.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		e.requests = append(e.requests, r.Method+" "+r.URL.Path)
		e.auth = append(e.auth, r.Header.Get("Authorization"))
		status := e.status
		limited := e.limited > 0
		if limited {
			e.limited--
		}
		products := e.products
		e.mu.Unlock()
		if limited {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":"rate_limited","message":"Limit exceeded"}`)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/admin/default-tier":
			_, _ = io.WriteString(w, `{"id":""}`)
		case r.Method == http.MethodGet && r.URL.Path == "/admin/products":
			if products == "" {
				products = "[]"
			}
			_, _ = io.WriteString(w, products)
		case r.Method == http.MethodGet:
			_, _ = io.WriteString(w, `[]`)
		case r.Method == http.MethodPut && r.URL.Path == "/admin/default-tier":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPut:
			w.WriteHeader(http.StatusOK)
		default:
			if status == 0 {
				status = http.StatusCreated
			}
			w.WriteHeader(status)
			if status >= 400 {
				_, _ = io.WriteString(w, `{"error":"invalid_request","message":"bad"}`)
			}
		}
	}))
	t.Cleanup(e.srv.Close)
	return e
}

func (e *entServer) withSession(t *testing.T, token string, loginErr error) {
	t.Helper()
	old := resolveAPIRequest
	resolveAPIRequest = func(service string) (string, func(context.Context) (string, error), error) {
		if service != "entitlements" {
			t.Errorf("resolved service %q, want entitlements", service)
		}
		return e.srv.URL, func(context.Context) (string, error) { return token, loginErr }, nil
	}
	t.Cleanup(func() { resolveAPIRequest = old })
}

func seedCommand(t *testing.T, args ...string) *cobra.Command {
	t.Helper()
	oldFile, oldPrices, oldDry, oldCurl, oldDelay, oldSleep := flagEntitlementsSeedFile, flagEntitlementsSeedPrices, flagEntitlementsSeedDryRun, flagCurl, flagEntitlementsSeedDelay, entitlementsSleep
	t.Cleanup(func() {
		flagEntitlementsSeedFile, flagEntitlementsSeedPrices, flagEntitlementsSeedDryRun, flagCurl = oldFile, oldPrices, oldDry, oldCurl
		flagEntitlementsSeedDelay, entitlementsSleep = oldDelay, oldSleep
	})
	entitlementsSleep = func(context.Context, time.Duration) error { return nil }
	ent := newEntitlementsCommand()
	seed, _, err := ent.Find([]string{"seed"})
	if err != nil || seed == ent {
		t.Fatalf("seed subcommand not found: %v", err)
	}
	if err := seed.Flags().Set("delay", "0s"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(args); i += 2 {
		if err := seed.Flags().Set(args[i], args[i+1]); err != nil {
			t.Fatal(err)
		}
	}
	return seed
}

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func runSeed(c *cobra.Command) (string, error) {
	var out bytes.Buffer
	c.SetContext(context.Background())
	c.SetOut(&out)
	c.SetErr(&out)
	err := c.RunE(c, nil)
	return out.String(), err
}

func exitCode(err error) int {
	var ec ExitCoder
	if errors.As(err, &ec) {
		return ec.ExitCode()
	}
	return -1
}

func TestEntitlementsSeedCreatesRowsAsTheLoggedInUser(t *testing.T) {
	e := newEntServer(t)
	e.withSession(t, "admin-token", nil)
	c := seedCommand(t, "file", writeTemp(t, "seed.json", entSeed))

	out, err := runSeed(c)
	if err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	for _, want := range []string{"created  key a.b", "created  tier free", "created  default tier free", "created  product p"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	for _, a := range e.auth {
		if a != "Bearer admin-token" {
			t.Errorf("Authorization = %q", a)
		}
	}
	want := "GET /admin/keys,GET /admin/tiers,GET /admin/products,GET /admin/default-tier,POST /admin/keys,POST /admin/tiers,PUT /admin/default-tier,POST /admin/products"
	if got := strings.Join(e.requests, ","); got != want {
		t.Errorf("requests = %s", got)
	}
}

func TestEntitlementsSeedLeavesExistingRowsAlone(t *testing.T) {
	e := newEntServer(t)
	e.status = http.StatusConflict
	e.withSession(t, "t", nil)
	out, err := runSeed(seedCommand(t, "file", writeTemp(t, "seed.json", entSeed)))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"exists   key a.b", "exists   tier free", "exists   product p"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "created  key") || strings.Contains(out, "created  tier") || strings.Contains(out, "created  product") {
		t.Errorf("output:\n%s", out)
	}
}

func TestEntitlementsSeedDryRunWritesNothing(t *testing.T) {
	e := newEntServer(t)
	e.withSession(t, "t", nil)
	prices := writeTemp(t, "prices.json", entPrices)
	out, err := runSeed(seedCommand(t, "file", writeTemp(t, "seed.json", entSeed), "prices", prices, "dry-run", "true"))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range e.requests {
		if !strings.HasPrefix(r, "GET") {
			t.Errorf("dry run sent %s", r)
		}
	}
	if !strings.Contains(out, "created  product p") {
		t.Errorf("output:\n%s", out)
	}
}

func TestEntitlementsSeedSendsAFreshProductWithItsPrice(t *testing.T) {
	e := newEntServer(t)
	e.withSession(t, "t", nil)
	out, err := runSeed(seedCommand(t, "file", writeTemp(t, "seed.json", entSeed), "prices", writeTemp(t, "prices.json", entPrices)))
	if err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if !strings.Contains(out, "created  product p") || e.requests[len(e.requests)-1] != "POST /admin/products" {
		t.Errorf("requests = %v\n%s", e.requests, out)
	}
}

func TestEntitlementsSeedPricesAnExistingProductThatHasNone(t *testing.T) {
	e := newEntServer(t)
	e.products = `[{"id":"p","name":"P","billing":"one_off","entitlements":{"a.b":1}}]`
	e.withSession(t, "t", nil)
	out, err := runSeed(seedCommand(t, "file", writeTemp(t, "seed.json", entSeed), "prices", writeTemp(t, "prices.json", entPrices)))
	if err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if !strings.Contains(out, "created  price p") || e.requests[len(e.requests)-1] != "PUT /admin/products/p" {
		t.Errorf("requests = %v\n%s", e.requests, out)
	}
}

func TestEntitlementsSeedReportsADifferentExistingPriceAsSkipped(t *testing.T) {
	e := newEntServer(t)
	e.products = `[{"id":"p","name":"P","billing":"one_off","stripe_price_id":"price_live","entitlements":{"a.b":1}}]`
	e.withSession(t, "t", nil)
	out, err := runSeed(seedCommand(t, "file", writeTemp(t, "seed.json", entSeed), "prices", writeTemp(t, "prices.json", entPrices)))
	if err != nil || !strings.Contains(out, "skipped  price p") {
		t.Errorf("err = %v\n%s", err, out)
	}
	for _, r := range e.requests {
		if strings.HasPrefix(r, "PUT /admin/products") {
			t.Errorf("a live price was overwritten: %s", r)
		}
	}
}

func TestEntitlementsSeedWaitsOutARateLimitAndSaysSo(t *testing.T) {
	e := newEntServer(t)
	e.limited = 1
	e.withSession(t, "t", nil)
	c := seedCommand(t, "file", writeTemp(t, "seed.json", entSeed))
	var waits []time.Duration
	entitlementsSleep = func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }
	out, err := runSeed(c)
	if err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if len(waits) != 1 || waits[0] != time.Second || !strings.Contains(out, "rate limited") {
		t.Errorf("waits = %v\n%s", waits, out)
	}
}

func TestEntitlementsSeedWithoutASessionExitsWithLoginHintAndSendsNothing(t *testing.T) {
	e := newEntServer(t)
	e.withSession(t, "", auth.ErrNotLoggedIn)
	_, err := runSeed(seedCommand(t, "file", writeTemp(t, "seed.json", entSeed)))
	if exitCode(err) != 3 || !strings.Contains(err.Error(), "auth login") {
		t.Fatalf("err = %v (exit %d)", err, exitCode(err))
	}
	if len(e.requests) != 0 {
		t.Errorf("requests = %v", e.requests)
	}
}

func TestEntitlementsSeedBadInputsAreUsageErrors(t *testing.T) {
	e := newEntServer(t)
	e.withSession(t, "t", nil)
	good := writeTemp(t, "seed.json", entSeed)
	cases := map[string][]string{
		"missing seed file":   {"file", filepath.Join(t.TempDir(), "nope.json")},
		"invalid seed":        {"file", writeTemp(t, "bad.json", `{"unknown": 1}`)},
		"missing prices file": {"file", good, "prices", filepath.Join(t.TempDir(), "nope.json")},
		"invalid prices":      {"file", good, "prices", writeTemp(t, "p.json", `{"products":{"p":""}}`)},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := runSeed(seedCommand(t, args...)); exitCode(err) != 2 {
				t.Errorf("err = %v (exit %d), want usage error", err, exitCode(err))
			}
		})
	}
	if len(e.requests) != 0 {
		t.Errorf("requests = %v", e.requests)
	}
}

func TestEntitlementsSeedRejectsCurl(t *testing.T) {
	e := newEntServer(t)
	e.withSession(t, "t", nil)
	c := seedCommand(t, "file", writeTemp(t, "seed.json", entSeed))
	flagCurl = true
	if _, err := runSeed(c); exitCode(err) != 2 {
		t.Errorf("err = %v, want usage error", err)
	}
}

func TestEntitlementsSeedServerErrorFailsWithItsMessage(t *testing.T) {
	e := newEntServer(t)
	e.status = http.StatusBadRequest
	e.withSession(t, "t", nil)
	_, err := runSeed(seedCommand(t, "file", writeTemp(t, "seed.json", entSeed)))
	if err == nil || !strings.Contains(err.Error(), "status 400") {
		t.Errorf("err = %v", err)
	}
}

func TestEntitlementsSeedRequiresFileFlag(t *testing.T) {
	ent := newEntitlementsCommand()
	seed, _, _ := ent.Find([]string{"seed"})
	if f := seed.Flag("file"); f == nil || f.Annotations[cobra.BashCompOneRequiredFlag] == nil {
		t.Error("--file is not marked required")
	}
}

func TestEntitlementsIsRegisteredOnTheRootCommand(t *testing.T) {
	buildTree()
	c, _, err := rootCmd.Find([]string{"entitlements", "seed"})
	if err != nil || c.Name() != "seed" {
		t.Errorf("find = %v, %v", c, err)
	}
}

func TestEntitlementsSeedReplacePricesNeedsPrices(t *testing.T) {
	e := newEntServer(t)
	e.withSession(t, "t", nil)
	_, err := runSeed(seedCommand(t, "file", writeTemp(t, "seed.json", entSeed), "replace-prices", "true"))
	if exitCode(err) != 2 || !strings.Contains(err.Error(), "--prices") {
		t.Errorf("err = %v (exit %d)", err, exitCode(err))
	}
	if len(e.requests) != 0 {
		t.Errorf("requests = %v", e.requests)
	}
}

func TestEntitlementsSeedReplacesAnExistingPriceAndSaysSo(t *testing.T) {
	for _, dry := range []bool{false, true} {
		t.Run(fmt.Sprintf("dry-run=%v", dry), func(t *testing.T) {
			e := newEntServer(t)
			e.products = `[{"id":"p","name":"P","billing":"one_off","stripe_price_id":"members_10","entitlements":{"a.b":1}}]`
			e.withSession(t, "t", nil)
			args := []string{"file", writeTemp(t, "seed.json", entSeed), "prices", writeTemp(t, "prices.json", `{"products":{"p":"price_real"}}`), "replace-prices", "true"}
			if dry {
				args = append(args, "dry-run", "true")
			}
			out, err := runSeed(seedCommand(t, args...))
			if err != nil {
				t.Fatalf("err = %v\n%s", err, out)
			}
			puts := 0
			for _, r := range e.requests {
				if r == "PUT /admin/products/p" {
					puts++
				}
			}
			verb := "replaced"
			if dry {
				verb = "would replace"
			}
			if !strings.Contains(out, verb+" price p (members_10 -> price_real)") {
				t.Errorf("output:\n%s", out)
			}
			if (puts == 1) == dry {
				t.Errorf("PUT count = %d with dry-run=%v", puts, dry)
			}
		})
	}
}
