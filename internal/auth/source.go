package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"
)

// stderrWriter keeps warnings out of stdout so scripted pipelines that parse
// output stay clean.
var stderrWriter io.Writer = os.Stderr

// AuthError marks failures that need `auth login` again. The command layer
// maps them to exit code 3.
type AuthError struct{ Msg string }

func (e *AuthError) Error() string { return e.Msg }

// IsAuthRequired reports whether err should exit 3 with a login hint.
func IsAuthRequired(err error) bool {
	var ae *AuthError
	if errors.As(err, &ae) {
		return true
	}
	return errors.Is(err, ErrNotLoggedIn) || errors.Is(err, ErrExpired) || errors.Is(err, ErrDenied)
}

// SessionSource supplies bearer tokens for one logged-in session, refreshing
// transparently and persisting rotations.
type SessionSource struct {
	Cfg   *Config
	HTTP  *http.Client
	Store Store

	mu      sync.Mutex
	cached  AccessToken
	session *Session

	// RotateSaveErr records a failed save after refresh-token rotation: the
	// current run keeps working, but the next run will need to log in again.
	RotateSaveErr error
}

// Token returns a valid bearer token, refreshing on expiry or first use.
func (s *SessionSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	if s.cached.Valid(now) {
		return s.cached.Token, nil
	}

	sess := s.session
	if sess == nil {
		loaded, err := s.Store.Load()
		if err != nil {
			return "", err
		}
		sess = loaded
		s.session = sess
	}

	tok, err := RefreshAccessToken(ctx, s.HTTP, s.Cfg, sess.RefreshToken)
	if err != nil {
		if errors.Is(err, ErrExpired) || errors.Is(err, ErrDenied) {
			_ = s.Store.Delete()
			s.session = nil
			return "", &AuthError{Msg: "your session has been revoked or expired: run 'sweetrpg auth login'"}
		}
		return "", err
	}

	s.cached = AccessToken{
		Token:     tok.AccessToken,
		ExpiresAt: now.Add(time.Duration(tok.ExpiresIn) * time.Second),
	}
	if tok.RefreshToken != sess.RefreshToken {
		sess.RefreshToken = tok.RefreshToken
		if saveErr := s.Store.Save(*sess); saveErr != nil && s.RotateSaveErr == nil {
			s.RotateSaveErr = saveErr
		}
	}
	return s.cached.Token, nil
}

// Login performs the full device flow and persists the resulting session.
// sleep is injectable for tests; production passes SleepContext. openURL,
// when non-nil, is tried with the verification URL as a convenience.
func Login(ctx context.Context, hc *http.Client, cfg *Config, st Store, sleep func(context.Context, time.Duration) error, openURL func(string)) (*Claims, error) {
	dc, err := requestDeviceCode(ctx, hc, cfg)
	if err != nil {
		return nil, err
	}
	target := dc.VerificationURIComplete
	if target == "" {
		target = dc.VerificationURI
	}
	fmt.Printf("Open %s\nEnter code: %s\n", target, dc.UserCode)
	if openURL != nil && target != "" {
		openURL(target)
	}
	fmt.Println("Waiting for authorization...")

	tok, err := AwaitAuthorization(ctx, hc, cfg, dc, sleep)
	if err != nil {
		return nil, err
	}
	if tok.RefreshToken == "" {
		return nil, fmt.Errorf("login succeeded but no refresh token was granted; check offline_access scope")
	}
	claims, err := ParseIDTokenClaims(tok.IDToken)
	if err != nil {
		return nil, err
	}
	sess := Session{Account: claims.Subject, Email: claims.Email, RefreshToken: tok.RefreshToken}
	if err := st.Save(sess); err != nil {
		// Refuse-to-persist fallback: never fall back to plaintext files.
		return claims, fmt.Errorf("credentials could not be saved to the OS keychain (%v); they work for this command only and you must log in again next time", err)
	}
	return claims, nil
}

// Logout revokes the stored refresh token server-side (best effort) and
// deletes local credentials. Missing credentials are not an error, so logout
// is idempotent; the bool reports whether credentials existed.
func Logout(ctx context.Context, hc *http.Client, cfg *Config, st Store) (bool, error) {
	sess, err := st.Load()
	if errors.Is(err, ErrNotLoggedIn) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	if revokeErr := RevokeRefreshToken(ctx, hc, cfg, sess.RefreshToken); revokeErr != nil {
		_, _ = fmt.Fprintf(stderrWriter, "warning: server-side revocation failed (%v); removing local credentials anyway\n", revokeErr)
	}
	return true, st.Delete()
}
