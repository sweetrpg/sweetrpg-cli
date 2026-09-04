package cmd

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sweetrpg/catalog-cli/internal/dtrpg"
)

// dtrpgAuthServer stands in for the DriveThruRPG auth_key endpoint so
// buildDTRPGClient can run through the real SDK without network access.
func dtrpgAuthServer(t *testing.T, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"message":"rejected"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"jwt","refreshToken":"r","refreshTokenTTL":9999999999}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// withImportSeams swaps the import command's test seams and restores them.
func withImportSeams(t *testing.T, store dtrpg.KeyStore, base string) {
	t.Helper()
	oldStore, oldBase := dtrpgKeyStore, dtrpgLoginBase
	oldSecret, oldLine, oldCred := promptSecret, promptLine, credentialLogin
	oldCreds := flagDTRPGCredentials
	dtrpgKeyStore = func() dtrpg.KeyStore { return store }
	dtrpgLoginBase = base
	t.Cleanup(func() {
		dtrpgKeyStore, dtrpgLoginBase = oldStore, oldBase
		promptSecret, promptLine, credentialLogin = oldSecret, oldLine, oldCred
		flagDTRPGCredentials = oldCreds
	})
}

func runImportChild(t *testing.T, name string, args ...string) (string, error) {
	t.Helper()
	buildTree()
	child := importChildren[name]
	if child == nil {
		t.Fatalf("import child %q missing", name)
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

	out, err := runImportChild(t, "login")
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

	_, err := runImportChild(t, "login")
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

	_, err := runImportChild(t, "login")
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

	out, err := runImportChild(t, "login")
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

	if _, err := runImportChild(t, "logout"); err != nil {
		t.Fatalf("logout with key: %v", err)
	}
	if _, err := store.LoadKey(); !errors.Is(err, dtrpg.ErrNoKey) {
		t.Errorf("key still present after logout")
	}
	if _, err := runImportChild(t, "logout"); err != nil {
		t.Fatalf("logout without key must be a no-op, got %v", err)
	}
}

// brokenKeyStore fails every persist so the keychain-unavailable path can be
// exercised.
type brokenKeyStore struct{}

func (brokenKeyStore) SaveKey(string) error     { return errors.New("secret service unavailable") }
func (brokenKeyStore) LoadKey() (string, error) { return "", dtrpg.ErrNoKey }
func (brokenKeyStore) DeleteKey() error         { return nil }
