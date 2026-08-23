package cmd

import (
	"sync"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "sweetrpg-catalog",
	Short: "Command-line client for the SweetRPG catalog service",
}

var buildOnce sync.Once

// buildTree attaches generated subcommands. It runs lazily (not in init) so
// the entity registry is fully populated regardless of file init order.
func buildTree() {
	buildOnce.Do(func() {
		rootCmd.PersistentFlags().StringVar(&flagAPIURL, "api-url", "", "catalog API base URL (overrides env and config file)")
		rootCmd.PersistentFlags().StringVar(&flagAssetsWebURL, "assets-web-url", "", "assets-web base URL for asset uploads")
		rootCmd.PersistentFlags().BoolVar(&flagYes, "yes", false, "assume non-interactive mode; fail on ambiguity with the candidate list")
		rootCmd.AddCommand(newAddCommand())
		rootCmd.AddCommand(newEditCommand())
		rootCmd.AddCommand(newViewCommand())
		rootCmd.AddCommand(newDeleteCommand())
		rootCmd.AddCommand(newLinkCommand())
		rootCmd.AddCommand(newUnlinkCommand())
	})
}

// Execute runs the root command. Errors returned by subcommands are printed by
// cobra; the exit code is decided here so subcommands can signal specific codes.
func Execute() error {
	buildTree()
	return rootCmd.Execute()
}
