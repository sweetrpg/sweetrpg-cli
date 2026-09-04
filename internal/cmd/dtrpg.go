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

// dtrpgChildren is kept here so tests can drive login/logout directly,
// matching the pattern the generated entity commands use.
var dtrpgChildren = map[string]*cobra.Command{}

// newDTRPGCommand builds a top-level `dtrpg` command: one DriveThruRPG login
// shared by every consumer that needs it (`catalog import dtrpg library`,
// `game-room import dtrpg`), rather than a separate credential per consumer.
// It's one external account either way; there's nothing to isolate.
func newDTRPGCommand() *cobra.Command {
	dtrpgCmd := &cobra.Command{
		Use:   "dtrpg",
		Short: "Manage your DriveThruRPG login",
		Long: "Store or remove the DriveThruRPG application key used by every command that\n" +
			"imports from your DriveThruRPG library (`catalog import dtrpg library`,\n" +
			"`game-room import dtrpg`). One login covers both.",
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

	dtrpgCmd.AddCommand(login, logout)

	dtrpgChildren["login"] = login
	dtrpgChildren["logout"] = logout
	return dtrpgCmd
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
