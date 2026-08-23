//go:build integration

package auth

import (
	"errors"
	"testing"
)

// TestKeychainRealRoundTrip exercises the actual macOS Keychain via go-keyring.
// It is excluded from normal runs because it writes to the login keychain and
// may trigger an access prompt. Run explicitly:
//
//	go test -tags=integration -run TestKeychainRealRoundTrip ./internal/auth/
func TestKeychainRealRoundTrip(t *testing.T) {
	st := KeyringStore{}

	if err := st.Delete(); err != nil {
		t.Fatalf("pre-clean delete: %v", err)
	}

	sess := Session{Account: "integration-test-sub", Email: "it@example.com", RefreshToken: "rt-it"}
	if err := st.Save(sess); err != nil {
		t.Fatalf("save: %v", err)
	}
	t.Cleanup(func() { _ = st.Delete() })

	got, err := st.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Account != sess.Account || got.Email != sess.Email || got.RefreshToken != sess.RefreshToken {
		t.Errorf("roundtrip mismatch: got %+v", got)
	}

	if err := st.Delete(); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := st.Load(); !errors.Is(err, ErrNotLoggedIn) {
		t.Errorf("after delete want ErrNotLoggedIn, got %v", err)
	}
}
