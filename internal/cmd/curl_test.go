package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/sweetrpg/sweetrpg-cli/internal/auth"
	"github.com/sweetrpg/sweetrpg-cli/internal/client"
)

func runCaptured(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	old := curlOut
	curlOut = &buf
	t.Cleanup(func() { curlOut = old })
	fn()
	return buf.String()
}

func TestRenderCurlMasksTokenAndEscapesQuotes(t *testing.T) {
	req, err := http.NewRequest(http.MethodPatch, "http://api.test/volumes/abc?dry=1", strings.NewReader(`{"title":"it's"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer secret-token")
	req.Header.Set("Content-Type", client.MediaType)
	req.Header.Set("Accept", client.MediaType)

	out := renderCurl(req)

	for _, want := range []string{"-X PATCH", "'http://api.test/volumes/abc?dry=1'", "<redacted>", "it'\\''s"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered command missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "secret-token") {
		t.Errorf("rendered command leaked the bearer token:\n%s", out)
	}
}

func TestRenderCurlOmitsXFlagOnGet(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "http://api.test/publishers", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out := renderCurl(req); strings.Contains(out, "-X GET") || !strings.HasPrefix(strings.TrimSpace(out), "curl 'http://api.test/publishers'") {
		t.Errorf("GET should default to curl <url>, got:\n%s", out)
	}
}

func TestPeekBodyPrettyPrintsAndPreserves(t *testing.T) {
	raw := []byte(`{"title":"Book"}`)
	req, err := http.NewRequest(http.MethodPatch, "http://api.test/volumes/x", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	got := peekBody(req)
	if !strings.Contains(string(got), "\n  \"title\"") {
		t.Errorf("expected pretty-printed JSON, got %q", got)
	}
	again, err := req.GetBody()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = again.Close() }()
	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(again); err != nil || !bytes.Equal(buf.Bytes(), raw) {
		t.Errorf("body not preserved for replay: %q, %v", buf.Bytes(), err)
	}
}

// captureBuilders point every builder at an unroutable URL behind the capture
// transport: if any real dial were attempted the error would be a network
// failure, never the sentinel.
func captureBuilders(t *testing.T, tokens func(context.Context) (string, error)) {
	t.Helper()
	oldFlag := flagCurl
	flagCurl = true
	t.Cleanup(func() { flagCurl = oldFlag })
	oldBuilder, oldAnon := buildAPIClient, buildAnonClient
	buildAPIClient = func() (*client.Client, error) {
		c, err := client.New("http://127.0.0.1:1", tokens)
		if err == nil {
			withCurlCapture(&c.HTTP)
		}
		return c, err
	}
	buildAnonClient = buildAPIClient
	t.Cleanup(func() { buildAPIClient, buildAnonClient = oldBuilder, oldAnon })
	resetResolveState(t)
	// Other tests leave per-command flag state behind; clear it so --json/--yaml
	// and friends start fresh.
	for _, m := range []map[string]*cobra.Command{addChildren, editChildren, viewChildren, deleteChildren} {
		for _, child := range m {
			child.Flags().VisitAll(func(f *pflag.Flag) { f.Changed = false })
		}
	}
}

func TestCurlCaptureAbortsViewWithSentinel(t *testing.T) {
	captureBuilders(t, func(context.Context) (string, error) { return "", nil })

	child := viewChildren["publisher"]
	out := runCaptured(t, func() {
		err := child.RunE(child, []string{"507f1f77bcf86cd799439011"})
		if !isCurlExit(err) {
			t.Errorf("expected cURL sentinel, got %v", err)
		}
	})
	if !strings.Contains(out, "'http://127.0.0.1:1/publishers/507f1f77bcf86cd799439011'") ||
		!strings.HasPrefix(strings.TrimSpace(out), "curl") {
		t.Errorf("expected rendered GET in output, got:\n%s", out)
	}
}

func TestCurlCaptureRendersDelete(t *testing.T) {
	captureBuilders(t, func(context.Context) (string, error) { return "", nil })

	child := deleteChildren["publisher"]
	oldYes, oldForce := flagYes, flagForce
	flagYes, flagForce = true, true
	t.Cleanup(func() { flagYes, flagForce = oldYes, oldForce })

	out := runCaptured(t, func() {
		err := child.RunE(child, []string{"507f1f77bcf86cd799439011"})
		if !isCurlExit(err) {
			t.Errorf("expected cURL sentinel, got %v", err)
		}
	})
	if !strings.Contains(out, "-X DELETE") {
		t.Errorf("expected rendered DELETE in output, got:\n%s", out)
	}
}

func TestExecuteSwallowsCurlSentinel(t *testing.T) {
	oldDomain, oldClientID, oldAudience := auth.Domain, auth.ClientID, auth.Audience
	auth.Domain, auth.ClientID, auth.Audience = "", "", ""
	t.Cleanup(func() { auth.Domain, auth.ClientID, auth.Audience = oldDomain, oldClientID, oldAudience })
	resetResolveState(t)
	rootCmd.SetArgs([]string{"--api-url", "http://127.0.0.1:9", "--yes", "--curl", "catalog", "view", "publisher", "507f1f77bcf86cd799439011"})
	t.Cleanup(func() { rootCmd.SetArgs(nil); _ = rootCmd.PersistentFlags().Set("curl", "false") })

	// Nothing may reach stderr: no "Error:" line, no usage dump.
	oldErr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	errOut := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		errOut <- buf.String()
	}()
	defer func() { os.Stderr = oldErr }()

	out := runCaptured(t, func() {
		if err := Execute(); err != nil {
			t.Errorf("--curl run must exit cleanly, got %v", err)
		}
	})
	_ = w.Close()
	if got := <-errOut; strings.Contains(got, "Error:") || strings.Contains(got, "Usage:") || got != "" {
		t.Errorf("stderr must stay quiet during --curl runs, got:\n%s", got)
	}
	if !strings.Contains(out, "'http://127.0.0.1:9/publishers/507f1f77bcf86cd799439011'") {
		t.Errorf("unexpected output:\n%s", out)
	}
}

func TestIsCurlExitSurvivesWrapping(t *testing.T) {
	if isCurlExit(nil) || isCurlExit(errors.New("dial refused")) {
		t.Error("non-capture errors must not match")
	}
	if !isCurlExit(fmt.Errorf("view publisher: %w", errCurlEmitted)) {
		t.Error("wrapped sentinel must still match")
	}
}
