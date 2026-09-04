package cmd

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sweetrpg/sweetrpg-cli/internal/dtrpg"
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

// brokenKeyStore fails every persist so the keychain-unavailable path can be
// exercised.
type brokenKeyStore struct{}

func (brokenKeyStore) SaveKey(string) error     { return errors.New("secret service unavailable") }
func (brokenKeyStore) LoadKey() (string, error) { return "", dtrpg.ErrNoKey }
func (brokenKeyStore) DeleteKey() error         { return nil }

// TestImportDTRPGRelocatedUnderCatalog verifies the import command tree
// resolves at `catalog import dtrpg library`, not the old top-level `import
// dtrpg library` shape, without actually executing it (a real run touches
// the keychain and network).
func TestImportDTRPGRelocatedUnderCatalog(t *testing.T) {
	buildTree()
	found, _, err := rootCmd.Find([]string{"catalog", "import", "dtrpg", "library"})
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if found.Name() != "library" {
		t.Errorf("resolved to %q, want library", found.Name())
	}
	if _, _, err := rootCmd.Find([]string{"import", "dtrpg", "library"}); err == nil {
		t.Error("top-level `import dtrpg library` should no longer resolve")
	}
}
