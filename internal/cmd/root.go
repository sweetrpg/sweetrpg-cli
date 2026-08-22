package cmd

import "github.com/spf13/cobra"

var rootCmd = &cobra.Command{
	Use:   "sweetrpg-catalog",
	Short: "Command-line client for the SweetRPG catalog service",
}

// Execute runs the root command. Errors returned by subcommands are printed by
// cobra; the exit code is decided here so subcommands can signal specific codes.
func Execute() error {
	return rootCmd.Execute()
}
