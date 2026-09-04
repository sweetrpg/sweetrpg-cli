package cmd

import (
	"github.com/spf13/cobra"
)

// import subcommands are kept here so tests can drive them directly, matching
// the pattern the generated entity commands use.
var importChildren = map[string]*cobra.Command{}

func newImportCommand() *cobra.Command {
	imp := &cobra.Command{
		Use:   "import",
		Short: "Bulk-import catalog records from external sources",
	}
	dtrpgCmd := &cobra.Command{
		Use:   "dtrpg",
		Short: "Import from a DriveThruRPG library",
	}

	library := &cobra.Command{
		Use:   "library",
		Short: "Import owned DriveThruRPG products as catalog volumes",
		Args:  cobra.NoArgs,
		RunE:  runDTRPGLibrary,
	}
	library.Flags().BoolVar(&flagImportDryRun, "dry-run", false,
		"fetch the library and print the import plan without creating any record")
	library.Flags().BoolVar(&flagImportArchived, "include-archived", false,
		"also import products whose DriveThruRPG files are archived")
	library.Flags().Uint32Var(&flagImportPageSize, "page-size", 0,
		"DriveThruRPG page size for library retrieval (0 uses the server default)")
	library.Flags().BoolVar(&flagImportQuiet, "quiet", false,
		"suppress normal per-product progress output; only failures and the final summary print")
	library.Flags().BoolVar(&flagImportVerbose, "verbose", false,
		"print additional detail: DriveThruRPG page fetch progress, publisher resolution, cover attach successes")

	dtrpgCmd.AddCommand(library)
	imp.AddCommand(dtrpgCmd)

	importChildren["library"] = library
	return imp
}
