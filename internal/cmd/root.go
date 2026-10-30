package cmd

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "sweetrpg",
	Short: "Command-line client for the SweetRPG platform",
	Long: "Manage SweetRPG platform services from one authenticated session.\n\n" +
		"  sweetrpg catalog ...      catalog records: volumes, publishers, studios, ...\n" +
		"  sweetrpg api ...          generic authenticated request against any service\n" +
		"  sweetrpg entitlements ... entitlements catalog (seed)\n\n" +
		"Shell completion: sweetrpg completion [bash|zsh|fish|powershell]\n" +
		"Exit codes: 0 success, 1 error, 2 usage, 3 authentication, 4 API client error (4xx),\n" +
		"5 API server error (5xx).",
	// Usage text is for usage errors only; execute prints it for exit code 2.
	SilenceUsage: true,
}

var buildOnce sync.Once

// catalogCmd groups the entity-registry commands (add/edit/view/delete/link/
// unlink/search) under the catalog namespace.
var catalogCmd = &cobra.Command{
	Use:   "catalog",
	Short: "Manage SweetRPG catalog records",
	Long: "Manage SweetRPG catalog records: volumes, publishers, studios, persons, systems,\n" +
		"licenses, reviews, and contributions.\n\n" +
		"Entity commands share one shape:\n" +
		"  sweetrpg catalog add <type> <name> [property flags]\n" +
		"  sweetrpg catalog edit <type> <name-or-id> [property flags]\n" +
		"  sweetrpg catalog view <type> <name-or-id> [--json | --yaml]\n" +
		"  sweetrpg catalog delete <type> <name-or-id> [--force]\n\n" +
		"Links connect two entities (either argument order):\n" +
		"  sweetrpg catalog link volume \"Dungeon World\" publisher \"Evil Hat Productions\"\n\n" +
		"Name arguments resolve to record IDs; 24-hex IDs are used directly. Ambiguous names\n" +
		"prompt a picker, or fail with the candidate list when --yes is set.",
}

// buildTree attaches generated subcommands. It runs lazily (not in init) so
// the entity registry is fully populated regardless of file init order.
func buildTree() {
	buildOnce.Do(func() {
		rootCmd.PersistentFlags().StringVar(&flagAPIURL, "api-url", "", "catalog API base URL (overrides env and config file)")
		rootCmd.PersistentFlags().StringVar(&flagAssetsWebURL, "assets-web-url", "", "assets-web base URL for asset uploads")
		rootCmd.PersistentFlags().BoolVar(&flagYes, "yes", false, "assume non-interactive mode; fail on ambiguity with the candidate list")
		rootCmd.PersistentFlags().BoolVar(&flagCurl, "curl", false, "print the equivalent cURL command(s) instead of calling the API")
		catalogCmd.AddCommand(newAddCommand())
		catalogCmd.AddCommand(newEditCommand())
		catalogCmd.AddCommand(newViewCommand())
		catalogCmd.AddCommand(newDeleteCommand())
		catalogCmd.AddCommand(newLinkCommand())
		catalogCmd.AddCommand(newUnlinkCommand())
		catalogCmd.AddCommand(newSearchCommand())
		catalogCmd.AddCommand(newImportCommand())
		rootCmd.AddCommand(catalogCmd)
		rootCmd.AddCommand(newAPICommand())
		rootCmd.AddCommand(newEntitlementsCommand())
		rootCmd.AddCommand(newGameRoomCommand())
		rootCmd.AddCommand(newDTRPGCommand())
		rootCmd.AddCommand(newVersionCommand())
		rootCmd.Version = buildVersion()
	})
}

// Execute runs the root command with the process's streams. Results go to stdout; the error line
// goes to stderr, and the usage text follows it only when the error is a usage error.
func Execute() error { return execute(nil, os.Stdout, os.Stderr) }

// execute runs the command tree with the given arguments (nil means os.Args) and streams. The exit
// code is decided here so subcommands can signal specific codes: 2 usage, 3 authentication, 4 API
// client error, 5 API server error, 1 anything else.
func execute(args []string, stdout, stderr io.Writer) error {
	buildTree()
	rootCmd.SetOut(stdout)
	rootCmd.SetErr(stderr)
	if args != nil {
		rootCmd.SetArgs(args)
	}
	rootCmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return &ExitError{Code: 2, Err: err}
	})
	rootCmd.SilenceErrors = false // a --curl run silences errors; do not carry that into this one
	failed, err := rootCmd.ExecuteC()
	err = classifyExit(classifyUsage(err))
	// A --curl run that rendered its request(s) is a success, even though the
	// capture transport aborts the flow with a sentinel.
	if isCurlExit(err) {
		return nil
	}
	var ec ExitCoder
	if errors.As(err, &ec) && ec.ExitCode() == exitUsage && failed != nil {
		_, _ = fmt.Fprint(stderr, "\n", failed.UsageString())
	}
	return err
}

// Exit codes beyond the general 1.
const (
	exitUsage  = 2
	exitAuth   = 3
	exitClient = 4
	exitServer = 5
)

// statusCoder is an error that carries the HTTP status an API answered with.
type statusCoder interface {
	error
	HTTPStatus() int
}

// classifyExit gives an error from an API call its documented exit code. An error that already
// has one (usage, authentication) keeps it.
func classifyExit(err error) error {
	if err == nil {
		return nil
	}
	var ec ExitCoder
	if errors.As(err, &ec) {
		return err
	}
	var sc statusCoder
	if errors.As(err, &sc) {
		return &ExitError{Code: exitForStatus(sc.HTTPStatus()), Err: err}
	}
	return err
}

// exitForStatus maps an API response status to an exit code: 401 is an authentication failure,
// other 4xx are client errors, 5xx are server errors.
func exitForStatus(status int) int {
	switch {
	case status == http.StatusUnauthorized:
		return exitAuth
	case status >= 500:
		return exitServer
	case status >= 400:
		return exitClient
	}
	return 1
}

// classifyUsage maps cobra's untyped command/argument errors to the documented
// usage exit code. Cobra has no typed errors for these; the message shapes it
// produces for them are stable.
func classifyUsage(err error) error {
	if err == nil {
		return nil
	}
	var ec ExitCoder
	if errors.As(err, &ec) {
		return err
	}
	msg := err.Error()
	if strings.HasPrefix(msg, "unknown command ") || strings.Contains(msg, " arg(s)") ||
		strings.HasPrefix(msg, "required flag(s) ") || strings.HasPrefix(msg, "invalid argument ") ||
		strings.HasPrefix(msg, "unknown flag:") || strings.HasPrefix(msg, "unknown shorthand flag:") {
		return &ExitError{Code: 2, Err: err}
	}
	return err
}

// usageErr tags a validation failure as a usage error (exit code 2).
func usageErr(format string, args ...any) error {
	return &ExitError{Code: 2, Err: fmt.Errorf(format, args...)}
}
