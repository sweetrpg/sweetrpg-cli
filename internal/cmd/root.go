package cmd

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "sweetrpg",
	Short: "Command-line client for the SweetRPG platform",
	Long: "Manage SweetRPG platform services from one authenticated session.\n\n" +
		"  sweetrpg catalog ...      catalog records: volumes, publishers, studios, ...\n" +
		"  sweetrpg api ...          generic authenticated request against any service\n\n" +
		"Shell completion: sweetrpg completion [bash|zsh|fish|powershell]\n" +
		"Exit codes: 0 success, 1 error, 2 usage, 3 authentication.",
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
	})
}

// Execute runs the root command. Errors returned by subcommands are printed by
// cobra; the exit code is decided here so subcommands can signal specific codes.
func Execute() error {
	buildTree()
	rootCmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return &ExitError{Code: 2, Err: err}
	})
	err := classifyUsage(rootCmd.Execute())
	// A --curl run that rendered its request(s) is a success, even though the
	// capture transport aborts the flow with a sentinel.
	if isCurlExit(err) {
		return nil
	}
	return err
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
		strings.HasPrefix(msg, "unknown flag:") || strings.HasPrefix(msg, "unknown shorthand flag:") {
		return &ExitError{Code: 2, Err: err}
	}
	return err
}

// usageErr tags a validation failure as a usage error (exit code 2).
func usageErr(format string, args ...any) error {
	return &ExitError{Code: 2, Err: fmt.Errorf(format, args...)}
}
