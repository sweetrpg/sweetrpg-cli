package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/sweetrpg/sweetrpg-cli/internal/client"
)

// run executes the real command tree with captured streams, the way main does.
func run(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	oldService, oldFields, oldRaw, oldHeaders, oldCurl := flagAPIService, flagAPIFields, flagAPIRawField, flagAPIHeaders, flagCurl
	flagAPIService, flagAPIFields, flagAPIRawField, flagAPIHeaders, flagCurl = "", nil, nil, nil, false
	t.Cleanup(func() {
		flagAPIService, flagAPIFields, flagAPIRawField, flagAPIHeaders, flagCurl = oldService, oldFields, oldRaw, oldHeaders, oldCurl
	})
	// Flag variables keep the last run's values, so clear the ones these tests touch.
	oldSeed := [4]any{flagEntitlementsSeedFile, flagEntitlementsSeedPrices, flagEntitlementsSeedDryRun, flagEntitlementsSeedReplace}
	flagEntitlementsSeedFile, flagEntitlementsSeedPrices, flagEntitlementsSeedDryRun, flagEntitlementsSeedReplace = "", "", false, false
	t.Cleanup(func() {
		flagEntitlementsSeedFile, flagEntitlementsSeedPrices = oldSeed[0].(string), oldSeed[1].(string)
		flagEntitlementsSeedDryRun, flagEntitlementsSeedReplace = oldSeed[2].(bool), oldSeed[3].(bool)
	})
	var out, errOut bytes.Buffer
	err = execute(args, &out, &errOut)
	return out.String(), errOut.String(), err
}

func TestAPIErrorStatusPrintsBodyToStdoutAndNoUsage(t *testing.T) {
	body := `{"error":"confirmation_required","message":"confirm the plan change to go ahead"}`
	f := newAPIFixture(t, http.StatusConflict, body)
	f.withTokenSource(t, "tok")

	stdout, stderr, err := run(t, "api", "POST", "/billing/checkout", "--service", "entitlements", "--field", "product_id=m20")

	if strings.TrimSpace(stdout) != body {
		t.Errorf("stdout = %q, want the response body exactly", stdout)
	}
	if strings.Contains(stderr, "Usage:") || strings.Contains(stderr, "Flags:") || strings.Contains(stdout, "Usage:") {
		t.Errorf("a valid call that the API answered with an error printed usage:\n%s", stderr)
	}
	if !strings.Contains(stderr, "409") {
		t.Errorf("stderr should carry the short error line: %q", stderr)
	}
	if got := exitCodeOf(t, err); got != 4 {
		t.Errorf("exit code = %d, want 4 for an API 4xx", got)
	}
}

func TestAPIStatusMapsToDocumentedExitCodes(t *testing.T) {
	cases := map[int]int{
		http.StatusBadRequest:          4,
		http.StatusForbidden:           4,
		http.StatusNotFound:            4,
		http.StatusConflict:            4,
		http.StatusTooManyRequests:     4,
		http.StatusUnauthorized:        3,
		http.StatusInternalServerError: 5,
		http.StatusBadGateway:          5,
	}
	for status, want := range cases {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			f := newAPIFixture(t, status, `{"error":"x"}`)
			f.withTokenSource(t, "tok")
			_, stderr, err := run(t, "api", "GET", "/x", "--service", "catalog")
			if got := exitCodeOf(t, err); got != want {
				t.Errorf("status %d: exit code = %d, want %d", status, got, want)
			}
			if strings.Contains(stderr, "Usage:") {
				t.Errorf("status %d printed usage", status)
			}
		})
	}
}

func TestMissingSessionIsExit3WithoutUsage(t *testing.T) {
	f := newAPIFixture(t, http.StatusOK, "{}")
	f.withNoSession(t)
	_, stderr, err := run(t, "api", "GET", "/x", "--service", "catalog")
	if exitCodeOf(t, err) != 3 || strings.Contains(stderr, "Usage:") {
		t.Errorf("exit = %d, stderr:\n%s", exitCodeOf(t, err), stderr)
	}
}

func TestNetworkFailureIsExit1WithoutUsage(t *testing.T) {
	f := newAPIFixture(t, http.StatusOK, "{}")
	f.withTokenSource(t, "tok")
	f.srv.Close()
	_, stderr, err := run(t, "api", "GET", "/x", "--service", "catalog")
	if exitCodeOf(t, err) != 1 || strings.Contains(stderr, "Usage:") {
		t.Errorf("exit = %d, stderr:\n%s", exitCodeOf(t, err), stderr)
	}
}

func TestBadFlagStillPrintsUsageWithExit2(t *testing.T) {
	_, stderr, err := run(t, "api", "--bogus")
	if exitCodeOf(t, err) != 2 {
		t.Errorf("exit = %d, want 2", exitCodeOf(t, err))
	}
	if !strings.Contains(stderr, "unknown flag") || !strings.Contains(stderr, "Usage:") {
		t.Errorf("a bad flag should print the error and the usage:\n%s", stderr)
	}
}

func TestUnknownCommandAndMissingRequiredInputPrintUsage(t *testing.T) {
	for name, args := range map[string][]string{
		"unknown command":   {"widgets"},
		"missing --service": {"api", "GET", "/x"},
		"missing --file":    {"entitlements", "seed"},
		"too many args":     {"version", "extra"},
	} {
		t.Run(name, func(t *testing.T) {
			_, stderr, err := run(t, args...)
			if exitCodeOf(t, err) != 2 || !strings.Contains(stderr, "Usage:") {
				t.Errorf("exit = %d, stderr:\n%s", exitCodeOf(t, err), stderr)
			}
		})
	}
}

func TestEntitlementsSeedAPIErrorIsExit4WithoutUsage(t *testing.T) {
	e := newEntServer(t)
	e.status = http.StatusBadRequest
	e.withSession(t, "t", nil)
	_, stderr, err := run(t, "entitlements", "seed", "--file", writeTemp(t, "seed.json", entSeed), "--delay", "0s")
	if exitCodeOf(t, err) != 4 || strings.Contains(stderr, "Usage:") {
		t.Errorf("exit = %d, stderr:\n%s", exitCodeOf(t, err), stderr)
	}
}

func TestClassifyExitMapsTypedStatusErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"catalog 404", &client.APIError{StatusCode: 404}, 4},
		{"catalog 500", &client.APIError{StatusCode: 500}, 5},
		{"catalog 401", &client.APIError{StatusCode: 401}, 3},
		{"wrapped", fmt.Errorf("adding: %w", &client.APIError{StatusCode: 422}), 4},
		{"already coded", &ExitError{Code: 2, Err: &client.APIError{StatusCode: 500}}, 2},
		{"plain error", errors.New("boom"), 1},
	}
	for _, c := range cases {
		if got := exitCodeOf(t, classifyExit(c.err)); got != c.want {
			t.Errorf("%s: exit = %d, want %d", c.name, got, c.want)
		}
	}
	if classifyExit(nil) != nil {
		t.Error("nil became an error")
	}
}
