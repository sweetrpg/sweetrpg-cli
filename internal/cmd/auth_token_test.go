package cmd

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/sweetrpg/sweetrpg-cli/internal/auth"
)

func withTokenSource(t *testing.T, tokens func(context.Context) (string, error), err error) {
	t.Helper()
	orig := resolveAuthToken
	resolveAuthToken = func() (func(context.Context) (string, error), error) { return tokens, err }
	t.Cleanup(func() { resolveAuthToken = orig })
}

func runAuthToken(t *testing.T) (stdout, stderr string, err error) {
	t.Helper()
	var out, errBuf bytes.Buffer
	authTokenCmd.SetContext(context.Background())
	authTokenCmd.SetOut(&out)
	authTokenCmd.SetErr(&errBuf)
	err = authTokenCmd.RunE(authTokenCmd, nil)
	return out.String(), errBuf.String(), err
}

func TestAuthTokenPrintsOnlyTheAccessToken(t *testing.T) {
	withTokenSource(t, func(context.Context) (string, error) { return "eyJ.access.token", nil }, nil)
	out, errOut, err := runAuthToken(t)
	if err != nil {
		t.Fatal(err)
	}
	if out != "eyJ.access.token\n" || errOut != "" {
		t.Errorf("stdout = %q, stderr = %q, want only the token and a newline", out, errOut)
	}
}

func TestAuthTokenWhenNotLoggedInPrintsNothingAndExits3(t *testing.T) {
	withTokenSource(t, func(context.Context) (string, error) { return "", auth.ErrNotLoggedIn }, nil)
	out, _, err := runAuthToken(t)
	if out != "" {
		t.Errorf("stdout = %q, want nothing", out)
	}
	if !errors.Is(err, auth.ErrNotLoggedIn) || exitCodeOf(t, err) != 3 {
		t.Errorf("err = %v, exit %d, want not logged in with exit 3", err, exitCodeOf(t, err))
	}
}

func TestAuthTokenWhenTheSessionExpiredExits3(t *testing.T) {
	withTokenSource(t, func(context.Context) (string, error) { return "", auth.ErrExpired }, nil)
	if _, _, err := runAuthToken(t); exitCodeOf(t, err) != 3 {
		t.Errorf("err = %v, exit %d, want 3", err, exitCodeOf(t, err))
	}
}

func TestAuthTokenConfigErrorIsReportedWithoutOutput(t *testing.T) {
	boom := errors.New("bad config")
	withTokenSource(t, nil, boom)
	out, _, err := runAuthToken(t)
	if out != "" || !errors.Is(err, boom) {
		t.Errorf("stdout = %q, err = %v", out, err)
	}
}
