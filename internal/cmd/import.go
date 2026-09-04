package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/manifoldco/promptui"
	dtrpgauth "github.com/pilgrimagesoftware/dtrpg-sdk.go/auth"
	dtrpglib "github.com/pilgrimagesoftware/dtrpg-sdk.go/library"
	"github.com/spf13/cobra"
	"github.com/sweetrpg/sweetrpg-cli/internal/dtrpg"
)

// Test seams. Production wiring reaches the real keychain, the real SDK, and
// promptui; tests replace these to run without a terminal, a keychain, or the
// network.
var (
	dtrpgKeyStore    = func() dtrpg.KeyStore { return dtrpg.KeyringStore{} }
	dtrpgLoginBase   = "" // "" targets the production DriveThruRPG API
	buildDTRPGClient = func(ctx context.Context, appKey string) (*dtrpg.Session, error) {
		return dtrpg.NewSession(ctx, appKey, dtrpgLoginBase)
	}
	credentialLogin = func(ctx context.Context, email, password string) (string, error) {
		return dtrpgauth.LoginWithCredentials(ctx, email, password, dtrpglib.NewConfig(""))
	}
	promptSecret = func(label string) (string, error) {
		return (&promptui.Prompt{Label: label, Mask: '*'}).Run()
	}
	promptLine = func(label string) (string, error) {
		return (&promptui.Prompt{Label: label}).Run()
	}
)

var flagDTRPGCredentials bool

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

	login := &cobra.Command{
		Use:   "login",
		Short: "Store a DriveThruRPG application key in the OS keychain",
		Args:  cobra.NoArgs,
		RunE:  runDTRPGLogin,
	}
	login.Flags().BoolVar(&flagDTRPGCredentials, "credentials", false,
		"prompt for DriveThruRPG email and password and mint an application key instead of pasting one")

	logout := &cobra.Command{
		Use:   "logout",
		Short: "Delete the stored DriveThruRPG application key",
		Args:  cobra.NoArgs,
		RunE:  runDTRPGLogout,
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

	dtrpgCmd.AddCommand(login, logout, library)
	imp.AddCommand(dtrpgCmd)

	importChildren["login"] = login
	importChildren["logout"] = logout
	importChildren["library"] = library
	return imp
}

func runDTRPGLogin(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()

	appKey, err := obtainAppKey(ctx)
	if err != nil {
		return err
	}
	appKey = strings.TrimSpace(appKey)
	if appKey == "" {
		return usageErr("no DriveThruRPG application key provided")
	}

	// Validate before persisting: a bad key should never reach the keychain.
	if _, err := buildDTRPGClient(ctx, appKey); err != nil {
		return err
	}
	if err := dtrpgKeyStore().SaveKey(appKey); err != nil {
		return fmt.Errorf("could not save the DriveThruRPG key to the OS keychain (%w); nothing was stored", err)
	}
	cmd.Println("Stored DriveThruRPG application key.")
	return nil
}

// obtainAppKey returns an application key, either pasted at a masked prompt or
// minted from interactively-entered credentials. The password is read through
// the masked prompt only - never a flag or argv - and discarded after the
// exchange.
func obtainAppKey(ctx context.Context) (string, error) {
	if !flagDTRPGCredentials {
		return promptSecret("DriveThruRPG application key")
	}
	email, err := promptLine("DriveThruRPG email")
	if err != nil {
		return "", err
	}
	password, err := promptSecret("DriveThruRPG password")
	if err != nil {
		return "", err
	}
	key, err := credentialLogin(ctx, strings.TrimSpace(email), password)
	if err != nil {
		return "", fmt.Errorf("credential login failed: %w", err)
	}
	return key, nil
}

func runDTRPGLogout(cmd *cobra.Command, _ []string) error {
	if err := dtrpgKeyStore().DeleteKey(); err != nil {
		return err
	}
	cmd.Println("Removed the stored DriveThruRPG application key.")
	return nil
}
