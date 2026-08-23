package cmd

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/sweetrpg/catalog-cli/internal/auth"
	"github.com/sweetrpg/catalog-cli/internal/client"
)

func exitCodeOf(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	var ec ExitCoder
	if errors.As(err, &ec) {
		return ec.ExitCode()
	}
	return 1
}

func TestClassifyUsageMapsCobraCommandErrorsToExit2(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, 0},
		{"unknown command", errors.New(`unknown command "widget" for "sweetrpg-catalog add"`), 2},
		{"arg count", errors.New("accepts 1 arg(s), received 2"), 2},
		{"unknown flag", errors.New("unknown flag: --bogus"), 2},
		{"server error", errors.New("connection refused"), 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := exitCodeOf(t, classifyUsage(tc.err)); got != tc.want {
				t.Errorf("classifyUsage(%v) exit = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

func TestFlagErrorFuncWrapsAsUsageError(t *testing.T) {
	buildTree()
	var wrapped error
	rootCmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		wrapped = &ExitError{Code: 2, Err: err}
		return wrapped
	})
	fn := rootCmd.FlagErrorFunc()
	err := fn(rootCmd, errors.New("flag needs an argument: --api-url"))
	if exitCodeOf(t, err) != 2 || !strings.Contains(err.Error(), "flag needs an argument") {
		t.Errorf("want usage exit 2 preserving message, got %v", err)
	}
}

func TestLinkValidationErrorsAreUsageExit2(t *testing.T) {
	f := newScriptedFixture(t, ok200("{}"))
	if err := runLinkErr(t, true, "person", linkPersonID, "studio", linkPubID); exitCodeOf(t, err) != 2 {
		t.Errorf("invalid pairing exit = %d, want 2 (err=%v)", exitCodeOf(t, err), err)
	}
	if f.requests != 0 {
		t.Errorf("validation issued %d requests", f.requests)
	}
	if err := runLinkErr(t, true, "widget", "x", "volume", linkVolumeID); exitCodeOf(t, err) != 2 {
		t.Errorf("unknown type exit = %d, want 2", exitCodeOf(t, err))
	}
}

func TestEditWithoutFlagsIsUsageExit2AndMakesNoCalls(t *testing.T) {
	f := newCmdFixture(t, http.StatusOK, "{}")
	child := editChildren["volume"]
	child.Flags().VisitAll(func(fl *pflag.Flag) { fl.Changed = false })
	child.SetContext(context.Background())
	err := child.RunE(child, []string{linkVolumeID})
	if exitCodeOf(t, err) != 2 || !strings.Contains(err.Error(), "no properties to update") {
		t.Fatalf("want usage exit 2, got %d (%v)", exitCodeOf(t, err), err)
	}
	if f.requests != 0 {
		t.Errorf("usage check issued %d requests", f.requests)
	}
}

func TestViewFormatConflictIsUsageExit2(t *testing.T) {
	newCmdFixture(t, http.StatusOK, "{}")
	child := viewChildren["volume"]
	for name, on := range map[string]bool{"json": true, "yaml": true} {
		fl := child.Flags().Lookup(name)
		if fl == nil {
			t.Fatalf("missing %s flag", name)
		}
		if on {
			fl.Value.Set("true")
			fl.Changed = true
		} else {
			fl.Changed = false
		}
	}
	format, err := formatFromFlags(child)
	if format != formatHuman {
		t.Errorf("conflict must fall back to human, got %v", format)
	}
	if exitCodeOf(t, err) != 2 {
		t.Errorf("format conflict exit = %d, want 2", exitCodeOf(t, err))
	}
}

func TestCompletionSubcommandExists(t *testing.T) {
	buildTree()
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		rootCmd.SetArgs([]string{"completion", shell})
		if err := Execute(); err != nil {
			t.Errorf("completion %s failed: %v", shell, err)
		}
	}
}

func TestViewUsesAnonymousClientAndSucceedsLoggedOut(t *testing.T) {
	f := newCmdFixture(t, http.StatusOK, onePublisherJSON)
	oldBuilder := buildAPIClient
	buildAPIClient = func() (*client.Client, error) {
		return nil, errors.New("view must not use the authenticated client")
	}
	t.Cleanup(func() { buildAPIClient = oldBuilder })
	resetResolveState(t)

	child := viewChildren["publisher"]
	var out bytes.Buffer
	child.SetContext(context.Background())
	child.SetOut(&out)
	if err := child.RunE(child, []string{"507f1f77bcf86cd799439011"}); err != nil {
		t.Fatalf("view should work without a session: %v", err)
	}
	if f.requests == 0 {
		t.Fatal("view made no requests")
	}
}

func TestTokenFuncDowngradesNotLoggedInOnlyWhenOptional(t *testing.T) {
	source := &auth.SessionSource{Store: &auth.MemoryStore{}}
	ctx := context.Background()

	token, err := tokenFunc(source, false)(ctx)
	if err != nil || token != "" {
		t.Errorf("anonymous mode: want (\"\", nil), got (%q, %v)", token, err)
	}

	token, err = tokenFunc(source, true)(ctx)
	if exitCodeOf(t, err) != 3 {
		t.Errorf("auth-required mode: want exit 3, got %v", err)
	}
	if token != "" {
		t.Errorf("want no token, got %q", token)
	}
}
