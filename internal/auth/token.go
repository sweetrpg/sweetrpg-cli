package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Claims are the ID-token fields the CLI cares about. The token arrives
// directly from Auth0 over TLS in this process, so fields are read without
// signature verification - verification is Auth0's job here.
type Claims struct {
	Subject string `json:"sub"`
	Email   string `json:"email"`
}

// ParseIDTokenClaims decodes the payload segment of a JWT. It returns an
// error for malformed tokens rather than guessing.
func ParseIDTokenClaims(idToken string) (*Claims, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" {
		return nil, fmt.Errorf("id_token is not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decoding id_token payload: %w", err)
	}
	var c Claims
	if err := json.Unmarshal(payload, &c); err != nil {
		return nil, fmt.Errorf("parsing id_token claims: %w", err)
	}
	if c.Subject == "" {
		return nil, fmt.Errorf("id_token has no sub claim")
	}
	return &c, nil
}

// RefreshAccessToken exchanges a refresh token for a fresh token set. When
// Auth0 rotates the refresh token the new one is in RefreshToken; otherwise
// that field is empty and the caller keeps the old one.
func RefreshAccessToken(ctx context.Context, hc *http.Client, cfg *Config, refreshToken string) (*TokenResponse, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {cfg.ClientID},
		"refresh_token": {refreshToken},
	}
	var out TokenResponse
	if err := postForm(ctx, hc, cfg.tokenURL(), form, &out); err != nil {
		var te *endpointError
		if asEndpointError(err, &te) && (te.Code == "invalid_grant" || te.Code == "forbidden") {
			return nil, fmt.Errorf("%w: refresh token rejected by server (%s)", ErrExpired, te.Code)
		}
		return nil, fmt.Errorf("refreshing access token: %w", err)
	}
	if out.AccessToken == "" {
		return nil, fmt.Errorf("refresh response missing access_token")
	}
	if out.RefreshToken == "" {
		out.RefreshToken = refreshToken
	}
	return &out, nil
}

// RevokeRefreshToken asks Auth0 to invalidate the refresh token. Failures are
// returned but logout treats them as non-fatal.
func RevokeRefreshToken(ctx context.Context, hc *http.Client, cfg *Config, refreshToken string) error {
	form := url.Values{
		"client_id":     {cfg.ClientID},
		"client_secret": {""}, // omitted below when empty
		"token":         {refreshToken},
	}
	delete(form, "client_secret")
	return postForm(ctx, hc, cfg.revokeURL(), form, &struct{}{})
}

func asEndpointError(err error, target **endpointError) bool {
	te, ok := err.(*endpointError)
	if ok {
		*target = te
	}
	return ok
}

// AccessToken pairs a bearer token with its expiry for session caching.
type AccessToken struct {
	Token     string
	ExpiresAt time.Time
}

// Valid reports whether the token can still be used with margin to spare so
// requests never race the expiry clock mid-flight.
func (a AccessToken) Valid(now time.Time) bool {
	return a.Token != "" && now.Before(a.ExpiresAt.Add(-30*time.Second))
}
