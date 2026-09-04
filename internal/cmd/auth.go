package cmd

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/spf13/cobra"
	"github.com/sweetrpg/sweetrpg-cli/internal/auth"
	"github.com/sweetrpg/sweetrpg-cli/internal/config"
)

// resolveAuthConfig combines the config file's authTenant section with env
// vars and the build-time defaults via auth.ResolveConfig, so an operator
// can repoint the Auth0 tenant without a rebuild.
func resolveAuthConfig() (*auth.Config, error) {
	cfg, err := config.Load(config.Sources{Getenv: os.Getenv, HomeDir: os.UserHomeDir})
	if err != nil {
		return nil, err
	}
	return auth.ResolveConfig(cfg.AuthDomain, cfg.AuthClientID, cfg.AuthAudience)
}

// ExitCoder lets subcommands pick their process exit code. Auth failures are
// 3 so scripts can react without parsing stderr.
type ExitCoder interface {
	error
	ExitCode() int
}

// ExitError adapts any error to an exit code.
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string { return e.Err.Error() }
func (e *ExitError) ExitCode() int { return e.Code }
func (e *ExitError) Unwrap() error { return e.Err }

// authExit tags errors the user fixes by logging in again.
func authExit(err error) error {
	if auth.IsAuthRequired(err) {
		return &ExitError{Code: 3, Err: err}
	}
	return err
}

// openInBrowser tries the platform opener; failing silently is fine because
// the URL is always printed for manual opening.
func openInBrowser(u string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "linux":
		cmd = exec.Command("xdg-open", u)
	default:
		return
	}
	_ = cmd.Start()
}

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Manage login state",
}

var authLoginCmd = &cobra.Command{
	Use:   "login",
	Short: "Log in via your browser using a device code",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := resolveAuthConfig()
		if err != nil {
			return err
		}
		hc := &http.Client{Timeout: 15 * time.Second}
		claims, err := auth.Login(cmd.Context(), hc, cfg, auth.KeyringStore{}, auth.SleepContext, openInBrowser)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			return authExit(err)
		}
		who := claims.Email
		if who == "" {
			who = claims.Subject
		}
		cmd.Printf("Logged in as %s\n", who)
		return nil
	},
}

var authLogoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Revoke and remove stored credentials",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := resolveAuthConfig()
		if err != nil {
			return err
		}
		loggedOut, err := auth.Logout(cmd.Context(), &http.Client{Timeout: 15 * time.Second}, cfg, auth.KeyringStore{})
		if err != nil {
			return err
		}
		if loggedOut {
			cmd.Println("Logged out.")
		} else {
			cmd.Println("Not logged in.")
		}
		return nil
	},
}

func init() {
	authCmd.AddCommand(authLoginCmd)
	authCmd.AddCommand(authLogoutCmd)
	rootCmd.AddCommand(authCmd)
}
