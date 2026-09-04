package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/sweetrpg/sweetrpg-cli/internal/client"
	"gopkg.in/yaml.v3"
)

func TestMain(m *testing.M) {
	buildTree()
	os.Exit(m.Run())
}

// cmdFixture is a scripted catalog-api plus an overridden client builder.
type cmdFixture struct {
	t         *testing.T
	status    int
	body      string
	requests  int
	gotMethod string
	gotPath   string
	gotBody   []byte
}

func newCmdFixture(t *testing.T, status int, body string) *cmdFixture {
	t.Helper()
	f := &cmdFixture{t: t, status: status, body: body}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		f.requests++
		f.gotMethod, f.gotPath = r.Method, r.URL.Path
		f.gotBody = raw
		w.Header().Set("Content-Type", client.MediaType)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	oldBuilder := buildAPIClient
	oldAnonBuilder := buildAnonClient
	buildAPIClient = func() (*client.Client, error) {
		c, err := client.New(srv.URL, func(context.Context) (string, error) { return "test-token", nil })
		return c, err
	}
	buildAnonClient = buildAPIClient
	t.Cleanup(func() { buildAPIClient = oldBuilder; buildAnonClient = oldAnonBuilder })
	resetResolveState(t)
	for _, m := range []map[string]*cobra.Command{addChildren, editChildren, viewChildren, deleteChildren} {
		for _, child := range m {
			child.Flags().VisitAll(func(f *pflag.Flag) {
				f.Changed = false
				// Clear leftover values from earlier tests so stateless
				// assertions (like "no properties to update") stay valid.
				if f.DefValue == "" {
					_ = f.Value.Set("")
				}
			})
		}
	}
	return f
}

func runEntityCommand(t *testing.T, cmd *cobra.Command, args []string) string {
	t.Helper()
	var out bytes.Buffer
	cmd.SetContext(context.Background())
	cmd.SetOut(&out)
	if err := cmd.RunE(cmd, args); err != nil {
		t.Fatalf("command failed: %v", err)
	}
	return out.String()
}

// runEntityCommandExpectError runs a command and hands back its error instead
// of failing the test; use when the error text is the assertion.
func runEntityCommandExpectError(t *testing.T, cmd *cobra.Command, args []string) error {
	t.Helper()
	cmd.SetContext(context.Background())
	cmd.SetOut(io.Discard)
	return cmd.RunE(cmd, args)
}

const createdPublisherJSON = `{"data":{"type":"publisher","id":"pub777","attributes":{"name":"Evil Hat Productions"}}}`

func TestAddPublisherAppliesFlagsAndPrintsID(t *testing.T) {
	f := newCmdFixture(t, http.StatusCreated, createdPublisherJSON)
	child := addChildren["publisher"]
	if child == nil {
		t.Fatal("add publisher child missing")
	}
	if err := child.Flags().Set("website", "https://evilhat.com"); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	child.SetContext(context.Background())
	child.SetOut(&out)
	if err := child.RunE(child, []string{"Evil Hat Productions"}); err != nil {
		t.Fatalf("add failed: %v", err)
	}
	if got := strings.TrimSpace(out.String()); !strings.Contains(got, "Created publisher pub777") {
		t.Errorf("output %q missing created ID", got)
	}
	if f.gotMethod != http.MethodPost || f.gotPath != "/publishers" {
		t.Errorf("request %s %s", f.gotMethod, f.gotPath)
	}
	var payload map[string]any
	if err := json.Unmarshal(f.gotBody, &payload); err != nil {
		t.Fatalf("bad request body: %v\n%s", err, f.gotBody)
	}
	if payload["name"] != "Evil Hat Productions" ||
		payload["website"] != "https://evilhat.com" {
		t.Errorf("payload missing values: %v", payload)
	}
}

func TestAddUnknownTypeListsValidTypes(t *testing.T) {
	newCmdFixture(t, http.StatusOK, "{}")
	_, err := lookupEntity("spaceship")
	if err == nil {
		t.Fatal("expected unknown-type error")
	}
	for _, want := range []string{"unknown catalog type", "volume", "contribution"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q: %v", want, err)
		}
	}
}

const onePublisherJSON = `{"data":{"type":"publisher","id":"507f1f77bcf86cd799439011","attributes":{"name":"Evil Hat Productions","website":""}}}`

func TestEditWithoutFlagsIsUsageError(t *testing.T) {
	f := newCmdFixture(t, http.StatusOK, onePublisherJSON)
	child := editChildren["publisher"]
	err := child.RunE(child, []string{"507f1f77bcf86cd799439011"})
	if err == nil || !strings.Contains(err.Error(), "no properties to update") {
		t.Fatalf("want usage error, got %v", err)
	}
	if f.requests != 0 {
		t.Errorf("no-flag edit issued %d requests", f.requests)
	}
}

func TestEditSendsPatchAndReportsDisposition(t *testing.T) {
	patched := `{"data":{"type":"publisher","id":"507f1f77bcf86cd799439011","attributes":{"name":"Evil Hat Productions"},"meta":{"version":3,"state":"live"}}}`
	f := newCmdFixture(t, http.StatusOK, patched)
	child := editChildren["publisher"]
	if err := child.Flags().Set("website", "https://evilhat.com"); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	child.SetContext(context.Background())
	child.SetOut(&out)
	if err := child.RunE(child, []string{"507f1f77bcf86cd799439011"}); err != nil {
		t.Fatalf("edit failed: %v", err)
	}
	if f.gotMethod != http.MethodPatch {
		t.Errorf("method %s, want PATCH", f.gotMethod)
	}
	if got := out.String(); !strings.Contains(got, "Updated live record") {
		t.Errorf("disposition not surfaced: %q", got)
	}
}

func TestViewEmitsRecordJSON(t *testing.T) {
	f := newCmdFixture(t, http.StatusOK, onePublisherJSON)
	child := viewChildren["publisher"]
	if err := child.Flags().Set("json", "true"); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	child.SetContext(context.Background())
	child.SetOut(&out)
	if err := child.RunE(child, []string{"507f1f77bcf86cd799439011"}); err != nil {
		t.Fatalf("view failed: %v", err)
	}
	var record struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(out.Bytes(), &record); err != nil {
		t.Fatalf("view output is not JSON: %v\n%s", err, out.String())
	}
	if record.ID != "507f1f77bcf86cd799439011" || record.Name != "Evil Hat Productions" {
		t.Errorf("unexpected record: %+v", record)
	}
	if f.requests != 1 {
		t.Errorf("view made %d requests, want 1 (ID short-circuit)", f.requests)
	}
}

func TestViewResolvesByNameBeforeFetch(t *testing.T) {
	// First request (search) returns one match; second (get by ID) returns the record.
	responses := []string{
		`{"data":[{"type":"publisher","id":"507f1f77bcf86cd799439011","attributes":{"name":"Evil Hat Productions"}}]}`,
		onePublisherJSON,
	}
	var call int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", client.MediaType)
		if call < len(responses) {
			_, _ = io.WriteString(w, responses[call])
			call++
			return
		}
		t.Errorf("unexpected extra request %s %s", r.Method, r.URL.Path)
	}))
	t.Cleanup(srv.Close)
	oldBuilder := buildAPIClient
	oldAnonBuilder := buildAnonClient
	buildAPIClient = func() (*client.Client, error) {
		c, err := client.New(srv.URL, func(context.Context) (string, error) { return "tok", nil })
		return c, err
	}
	buildAnonClient = buildAPIClient
	t.Cleanup(func() { buildAPIClient = oldBuilder; buildAnonClient = oldAnonBuilder })
	resetResolveState(t)

	child := viewChildren["publisher"]
	var out bytes.Buffer
	child.SetContext(context.Background())
	child.SetOut(&out)
	if err := child.RunE(child, []string{"Evil Hat Productions"}); err != nil {
		t.Fatalf("view failed: %v", err)
	}
	if !strings.Contains(out.String(), `"507f1f77bcf86cd799439011"`) {
		t.Errorf("expected resolved record in output, got %s", out.String())
	}
}

func TestDeleteRefusedNonInteractiveWithoutForce(t *testing.T) {
	f := newCmdFixture(t, http.StatusNoContent, "")
	flagYes = true // scripted mode refuses even on unique resolution
	defer func() { flagYes = false }()
	child := deleteChildren["publisher"]
	err := child.RunE(child, []string{"507f1f77bcf86cd799439011"})
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("want non-interactive refusal, got %v", err)
	}
	if f.requests != 0 {
		t.Errorf("refused delete still issued %d requests", f.requests)
	}
}

func TestDeleteWithForceIssuesDeleteRequest(t *testing.T) {
	f := newCmdFixture(t, http.StatusNoContent, "")
	stdoutIsTTY = func() bool { return false }
	flagForce = true
	defer func() { flagForce = false }()
	child := deleteChildren["publisher"]
	var out bytes.Buffer
	child.SetContext(context.Background())
	child.SetOut(&out)
	if err := child.RunE(child, []string{"507f1f77bcf86cd799439011"}); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	if f.gotMethod != http.MethodDelete || f.requests != 1 {
		t.Errorf("requests=%d method=%s", f.requests, f.gotMethod)
	}
	if got := strings.TrimSpace(out.String()); !strings.Contains(got, "Deleted publisher") {
		t.Errorf("missing confirmation line: %q", got)
	}
}

func TestDispositionTextVariants(t *testing.T) {
	if got := dispositionText(nil); got != "" {
		t.Errorf("nil disposition = %q, want empty", got)
	}
	submitted := &client.WriteDisposition{Submitted: true}
	if got := dispositionText(submitted); got != "Submitted proposed change for review" {
		t.Errorf("submitted = %q", got)
	}
	withMsg := &client.WriteDisposition{Submitted: true, Message: "needs review"}
	if got := dispositionText(withMsg); got != "Submitted proposed change: needs review" {
		t.Errorf("submitted+msg = %q", got)
	}
	live := &client.WriteDisposition{Version: 7}
	if got := dispositionText(live); got != "Updated live record (version 7)" {
		t.Errorf("live = %q", got)
	}
}

func TestDeleteDeclineExitsCleanWithoutRequests(t *testing.T) {
	f := newCmdFixture(t, http.StatusNoContent, "")
	stdoutIsTTY = func() bool { return true }
	confirmPrompt = func(string) bool { return false }
	t.Cleanup(func() { confirmPrompt = interactiveConfirm })
	child := deleteChildren["publisher"]
	var out bytes.Buffer
	child.SetContext(context.Background())
	child.SetOut(&out)
	if err := child.RunE(child, []string{"507f1f77bcf86cd799439011"}); err != nil {
		t.Fatalf("decline must exit 0, got %v", err)
	}
	if f.requests != 0 {
		t.Errorf("declined delete issued %d requests", f.requests)
	}
	if got := out.String(); !strings.Contains(got, "unchanged") {
		t.Errorf("missing cancellation note: %q", got)
	}
}

func TestEditSurfacesServerRejectionMessage(t *testing.T) {
	newCmdFixture(t, http.StatusUnprocessableEntity,
		`{"errors":[{"detail":"submission cap reached for this record"}]}`)
	child := editChildren["publisher"]
	if err := child.Flags().Set("website", "https://evilhat.com"); err != nil {
		t.Fatal(err)
	}
	err := child.RunE(child, []string{"507f1f77bcf86cd799439011"})
	if err == nil || !strings.Contains(err.Error(), "submission cap reached") {
		t.Fatalf("want server message in error, got %v", err)
	}
}

func TestViewHumanIsDefault(t *testing.T) {
	newCmdFixture(t, http.StatusOK, onePublisherJSON)
	out := runEntityCommand(t, viewChildren["publisher"], []string{"507f1f77bcf86cd799439011"})
	if strings.Contains(out, "{") || strings.Contains(out, "AuditableVO") {
		t.Errorf("human output leaked raw structures:\n%s", out)
	}
	for _, want := range []string{"id:", "name:", "Evil Hat Productions"} {
		if !strings.Contains(out, want) {
			t.Errorf("human output missing %q:\n%s", want, out)
		}
	}
}

func TestViewYAMLOutputParses(t *testing.T) {
	newCmdFixture(t, http.StatusOK, onePublisherJSON)
	child := viewChildren["publisher"]
	if err := child.Flags().Set("yaml", "true"); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	child.SetContext(context.Background())
	child.SetOut(&out)
	if err := child.RunE(child, []string{"507f1f77bcf86cd799439011"}); err != nil {
		t.Fatalf("view failed: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("view output is not YAML: %v\n%s", err, out.String())
	}
	if doc["id"] != "507f1f77bcf86cd799439011" {
		t.Errorf("unexpected yaml doc: %v", doc)
	}
}

func TestViewJSONAndYAMLMutuallyExclusive(t *testing.T) {
	f := newCmdFixture(t, http.StatusOK, onePublisherJSON)
	child := viewChildren["publisher"]
	if err := child.Flags().Set("json", "true"); err != nil {
		t.Fatal(err)
	}
	if err := child.Flags().Set("yaml", "true"); err != nil {
		t.Fatal(err)
	}
	err := child.RunE(child, []string{"507f1f77bcf86cd799439011"})
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("want mutual-exclusion error, got %v", err)
	}
	if f.requests != 0 {
		t.Errorf("flag conflict issued %d requests", f.requests)
	}
}
