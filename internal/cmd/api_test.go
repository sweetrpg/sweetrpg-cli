package cmd

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/sweetrpg/sweetrpg-cli/internal/auth"
)

type apiFixture struct {
	t         *testing.T
	status    int
	body      string
	requests  int
	gotMethod string
	gotPath   string
	gotAuth   string
	gotBody   []byte
	gotHdr    http.Header
	srv       *httptest.Server
}

func (f *apiFixture) gotHeader(key string) string { return f.gotHdr.Get(key) }

func newAPIFixture(t *testing.T, status int, body string) *apiFixture {
	t.Helper()
	f := &apiFixture{t: t, status: status, body: body}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		f.requests++
		f.gotMethod, f.gotPath, f.gotAuth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		f.gotBody = raw
		f.gotHdr = r.Header
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// withTokenSource points resolveAPIRequest at the fixture server with a
// token func that always succeeds, restoring the real resolver afterward.
func (f *apiFixture) withTokenSource(t *testing.T, token string) {
	t.Helper()
	old := resolveAPIRequest
	resolveAPIRequest = func(service string) (string, func(context.Context) (string, error), error) {
		return f.srv.URL, func(context.Context) (string, error) { return token, nil }, nil
	}
	t.Cleanup(func() { resolveAPIRequest = old })
}

// withNoSession points resolveAPIRequest at the fixture server with a token
// func that always fails as not-logged-in.
func (f *apiFixture) withNoSession(t *testing.T) {
	t.Helper()
	old := resolveAPIRequest
	resolveAPIRequest = func(service string) (string, func(context.Context) (string, error), error) {
		return f.srv.URL, func(context.Context) (string, error) { return "", auth.ErrNotLoggedIn }, nil
	}
	t.Cleanup(func() { resolveAPIRequest = old })
}

// newTestAPICommand builds a fresh api command with --service defaulted to
// "catalog", restoring every package-level flag var afterward. newAPICommand
// re-binds flagAPIService et al. to their zero values on construction, so
// this must run before a test sets flagAPIFields/flagAPIRawField/flagCurl.
func newTestAPICommand(t *testing.T) *cobra.Command {
	t.Helper()
	oldService, oldFields, oldRaw, oldHeaders, oldCurl := flagAPIService, flagAPIFields, flagAPIRawField, flagAPIHeaders, flagCurl
	t.Cleanup(func() {
		flagAPIService, flagAPIFields, flagAPIRawField, flagAPIHeaders, flagCurl = oldService, oldFields, oldRaw, oldHeaders, oldCurl
	})
	c := newAPICommand()
	if err := c.Flags().Set("service", "catalog"); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestAPIGetSuccessPrintsBodyAndSetsAuth(t *testing.T) {
	f := newAPIFixture(t, http.StatusOK, `{"data":{"id":"123"}}`)
	f.withTokenSource(t, "test-token")
	c := newTestAPICommand(t)

	out := runEntityCommand(t, c, []string{"GET", "/volumes/123"})

	if strings.TrimSpace(out) != `{"data":{"id":"123"}}` {
		t.Errorf("output = %q", out)
	}
	if f.gotMethod != http.MethodGet || f.gotPath != "/volumes/123" {
		t.Errorf("request %s %s", f.gotMethod, f.gotPath)
	}
	if f.gotAuth != "Bearer test-token" {
		t.Errorf("Authorization = %q", f.gotAuth)
	}
}

func TestAPINonTwoXXPrintsBodyAndExitsNonZero(t *testing.T) {
	f := newAPIFixture(t, http.StatusNotFound, `{"error":"not found"}`)
	f.withTokenSource(t, "test-token")
	c := newTestAPICommand(t)

	var out strings.Builder
	c.SetContext(context.Background())
	c.SetOut(&out)
	err := c.RunE(c, []string{"GET", "/volumes/999"})

	if !strings.Contains(out.String(), `"error":"not found"`) {
		t.Errorf("output missing body: %q", out.String())
	}
	var ec ExitCoder
	if !errors.As(err, &ec) || ec.ExitCode() == 0 {
		t.Fatalf("want non-zero ExitCoder, got %v", err)
	}
}

func TestAPINoSessionExitsWithLoginHint(t *testing.T) {
	f := newAPIFixture(t, http.StatusOK, "{}")
	f.withNoSession(t)
	c := newTestAPICommand(t)

	err := runEntityCommandExpectError(t, c, []string{"GET", "/volumes/123"})

	var ec ExitCoder
	if !errors.As(err, &ec) || ec.ExitCode() != 3 {
		t.Fatalf("want exit code 3, got %v", err)
	}
	if !strings.Contains(err.Error(), "auth login") {
		t.Errorf("error should mention auth login: %v", err)
	}
	if f.requests != 0 {
		t.Errorf("no request should have been sent, got %d", f.requests)
	}
}

func TestAPIFieldFlagsBuildJSONBodyAndDefaultToPOST(t *testing.T) {
	f := newAPIFixture(t, http.StatusCreated, `{"data":{"id":"pub1"}}`)
	f.withTokenSource(t, "test-token")
	c := newTestAPICommand(t)
	flagAPIFields = []string{`name=Evil Hat`, "featured=true", "rank=7"}

	runEntityCommand(t, c, []string{"/publishers"})

	if f.gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", f.gotMethod)
	}
	if got := string(f.gotBody); !strings.Contains(got, `"name":"Evil Hat"`) ||
		!strings.Contains(got, `"featured":true`) || !strings.Contains(got, `"rank":7`) {
		t.Errorf("body = %s", got)
	}
}

func TestAPIRawFieldStaysString(t *testing.T) {
	f := newAPIFixture(t, http.StatusOK, "{}")
	f.withTokenSource(t, "test-token")
	c := newTestAPICommand(t)
	flagAPIRawField = []string{"code=007"}

	runEntityCommand(t, c, []string{"/publishers"})

	if got := string(f.gotBody); !strings.Contains(got, `"code":"007"`) {
		t.Errorf("body = %s, want raw string 007", got)
	}
}

func TestAPIExtraHeaderFlagIsSent(t *testing.T) {
	f := newAPIFixture(t, http.StatusOK, "{}")
	f.withTokenSource(t, "test-token")
	c := newTestAPICommand(t)
	flagAPIHeaders = []string{"X-Request-Id: abc123"}

	runEntityCommand(t, c, []string{"GET", "/users/me"})

	if got := f.gotHeader("X-Request-Id"); got != "abc123" {
		t.Errorf("X-Request-Id = %q, want abc123", got)
	}
}

func TestAPICurlPreviewWithSessionMasksTokenAndSendsNothing(t *testing.T) {
	f := newAPIFixture(t, http.StatusOK, "{}")
	f.withTokenSource(t, "super-secret-token")
	c := newTestAPICommand(t)
	flagCurl = true

	out := runCaptured(t, func() {
		if err := c.RunE(c, []string{"GET", "/volumes"}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	if !strings.Contains(out, "<redacted>") {
		t.Errorf("curl output should mask the token: %q", out)
	}
	if strings.Contains(out, "super-secret-token") {
		t.Errorf("curl output leaked the token: %q", out)
	}
	if f.requests != 0 {
		t.Errorf("no request should have been sent, got %d", f.requests)
	}
}

func TestAPICurlPreviewWithoutSessionOmitsAuthorizationHeader(t *testing.T) {
	f := newAPIFixture(t, http.StatusOK, "{}")
	f.withNoSession(t)
	c := newTestAPICommand(t)
	flagCurl = true

	out := runCaptured(t, func() {
		if err := c.RunE(c, []string{"GET", "/volumes"}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	if strings.Contains(out, "Authorization") {
		t.Errorf("curl output should omit Authorization: %q", out)
	}
	if f.requests != 0 {
		t.Errorf("no request should have been sent, got %d", f.requests)
	}
}
