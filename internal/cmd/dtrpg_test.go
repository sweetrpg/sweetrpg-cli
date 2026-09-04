package cmd

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/sweetrpg/sweetrpg-cli/internal/dtrpg"
)

func runDTRPGChild(t *testing.T, name string, args ...string) (string, error) {
	t.Helper()
	buildTree()
	child := dtrpgChildren[name]
	if child == nil {
		t.Fatalf("dtrpg child %q missing", name)
	}
	var out bytes.Buffer
	child.SetContext(context.Background())
	child.SetOut(&out)
	err := child.RunE(child, args)
	return out.String(), err
}

func TestDTRPGLoginStoresPastedKeyWithoutEchoing(t *testing.T) {
	auth := dtrpgAuthServer(t, http.StatusOK)
	store := &dtrpg.MemoryKeyStore{}
	withImportSeams(t, store, auth.URL)
	flagDTRPGCredentials = false
	promptSecret = func(string) (string, error) { return "  paste-key-123  ", nil }

	out, err := runDTRPGChild(t, "login")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if got, _ := store.LoadKey(); got != "paste-key-123" {
		t.Fatalf("stored key = %q, want trimmed paste-key-123", got)
	}
	if strings.Contains(out, "paste-key-123") {
		t.Errorf("key material leaked to stdout: %q", out)
	}
}

func TestDTRPGLoginRejectsInvalidKeyAndStoresNothing(t *testing.T) {
	auth := dtrpgAuthServer(t, http.StatusUnauthorized)
	store := &dtrpg.MemoryKeyStore{}
	withImportSeams(t, store, auth.URL)
	promptSecret = func(string) (string, error) { return "bad-key", nil }

	_, err := runDTRPGChild(t, "login")
	if err == nil || !strings.Contains(err.Error(), "DriveThruRPG login failed") {
		t.Fatalf("want login-failed error, got %v", err)
	}
	if _, loadErr := store.LoadKey(); !errors.Is(loadErr, dtrpg.ErrNoKey) {
		t.Errorf("invalid key was persisted")
	}
}

func TestDTRPGLoginKeychainUnavailableReportsAndPersistsNothing(t *testing.T) {
	auth := dtrpgAuthServer(t, http.StatusOK)
	withImportSeams(t, brokenKeyStore{}, auth.URL)
	promptSecret = func(string) (string, error) { return "good-key", nil }

	_, err := runDTRPGChild(t, "login")
	if err == nil || !strings.Contains(err.Error(), "OS keychain") {
		t.Fatalf("want keychain error, got %v", err)
	}
}

func TestDTRPGLoginCredentialsFlowMintsKey(t *testing.T) {
	auth := dtrpgAuthServer(t, http.StatusOK)
	store := &dtrpg.MemoryKeyStore{}
	withImportSeams(t, store, auth.URL)
	flagDTRPGCredentials = true
	promptLine = func(string) (string, error) { return "user@example.com", nil }
	promptSecret = func(label string) (string, error) { return "s3cret", nil }
	credentialLogin = func(_ context.Context, email, password string) (string, error) {
		if email != "user@example.com" || password != "s3cret" {
			return "", errors.New("unexpected credentials")
		}
		return "minted-key", nil
	}

	out, err := runDTRPGChild(t, "login")
	if err != nil {
		t.Fatalf("credentials login: %v", err)
	}
	if got, _ := store.LoadKey(); got != "minted-key" {
		t.Fatalf("stored key = %q, want minted-key", got)
	}
	if strings.Contains(out, "s3cret") || strings.Contains(out, "minted-key") {
		t.Errorf("secret material leaked to stdout: %q", out)
	}
}

func TestDTRPGLogoutIsIdempotent(t *testing.T) {
	store := &dtrpg.MemoryKeyStore{}
	_ = store.SaveKey("k")
	withImportSeams(t, store, "")

	if _, err := runDTRPGChild(t, "logout"); err != nil {
		t.Fatalf("logout with key: %v", err)
	}
	if _, err := store.LoadKey(); !errors.Is(err, dtrpg.ErrNoKey) {
		t.Errorf("key still present after logout")
	}
	if _, err := runDTRPGChild(t, "logout"); err != nil {
		t.Fatalf("logout without key must be a no-op, got %v", err)
	}
}

// TestDTRPGCommandIsTopLevelAndSharedByBothImports verifies `dtrpg
// login`/`logout` live at the top level (not under catalog or game-room),
// and that both `catalog import dtrpg library` and `game-room import dtrpg`
// resolve without their own login/logout children - one login, two
// consumers.
func TestDTRPGCommandIsTopLevelAndSharedByBothImports(t *testing.T) {
	buildTree()
	for _, path := range [][]string{
		{"dtrpg", "login"},
		{"dtrpg", "logout"},
	} {
		found, _, err := rootCmd.Find(path)
		if err != nil {
			t.Fatalf("Find(%v): %v", path, err)
		}
		if found.Name() != path[len(path)-1] {
			t.Errorf("Find(%v) resolved to %q", path, found.Name())
		}
	}
	for _, path := range [][]string{
		{"catalog", "import", "dtrpg", "login"},
		{"catalog", "import", "dtrpg", "logout"},
		{"game-room", "import", "dtrpg", "login"},
		{"game-room", "import", "dtrpg", "logout"},
	} {
		// Cobra's Find doesn't error on trailing unresolved args - it
		// returns the deepest matched command with the rest left over. A
		// namespaced login/logout no longer existing means the resolved
		// command's own name never matches "login"/"logout".
		found, _, err := rootCmd.Find(path)
		want := path[len(path)-1]
		if err == nil && found.Name() == want {
			t.Errorf("Find(%v) resolved to %q - login/logout should live only under top-level dtrpg", path, found.Name())
		}
	}
}
