package dtrpg

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewSessionExchangesKeyForToken(t *testing.T) {
	var gotPath, gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.URL.Query().Get("applicationKey")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"jwt-abc","refreshToken":"refresh-xyz","refreshTokenTTL":9999999999}`))
	}))
	defer srv.Close()

	sess, err := NewSession(context.Background(), "app-key-42", srv.URL)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if sess.lib == nil {
		t.Fatal("session has no library client")
	}
	if !strings.HasSuffix(gotPath, "/auth_key") {
		t.Errorf("auth path = %q, want .../auth_key", gotPath)
	}
	if gotKey != "app-key-42" {
		t.Errorf("applicationKey = %q, want app-key-42", gotKey)
	}
}

func TestNewSessionEmptyKeyRejected(t *testing.T) {
	if _, err := NewSession(context.Background(), "", ""); err != ErrNoKey {
		t.Fatalf("NewSession(\"\") = %v, want ErrNoKey", err)
	}
}

func TestNewSessionSurfacesAuthFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"bad key"}`))
	}))
	defer srv.Close()

	_, err := NewSession(context.Background(), "wrong", srv.URL)
	if err == nil || !strings.Contains(err.Error(), "DriveThruRPG login failed") {
		t.Fatalf("want login-failed error, got %v", err)
	}
}
