// Package auth implements login against Auth0 using the OAuth device
// authorization grant, refresh-token storage in the OS keychain, and a
// per-session TokenSource that refreshes transparently. Endpoints come from
// build-time variables so no tenant details ship in source.
package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Build-time settings. Empty means auth is not configured; commands that need
// it fail with a clear message instead of guessing a tenant.
var (
	Domain   string // e.g. "dev-abc123.us.auth0.com"
	ClientID string
	Audience string // catalog-api identifier
	Scopes   = "openid profile email offline_access"
)

// Config is one resolved tenant + client pairing. Tests build it directly;
// production resolves it from the vars above via DefaultConfig.
type Config struct {
	Domain   string
	ClientID string
	Audience string
	Scopes   string

	// PollIntervalFloor guards against servers reporting interval 0; RFC 8628
	// suggests treating anything under 5s as 5s.
	PollIntervalFloor time.Duration
}

// DefaultConfig resolves the build-time settings, falling back to
// SWEETRPG_AUTH_DOMAIN / SWEETRPG_AUTH_CLIENT_ID / SWEETRPG_AUTH_AUDIENCE for
// dev runs (plain `go run` bakes nothing in). It errors when neither source
// provides a full configuration.
func DefaultConfig() (*Config, error) {
	domain := firstNonEmpty(Domain, os.Getenv("SWEETRPG_AUTH_DOMAIN"))
	clientID := firstNonEmpty(ClientID, os.Getenv("SWEETRPG_AUTH_CLIENT_ID"))
	audience := firstNonEmpty(Audience, os.Getenv("SWEETRPG_AUTH_AUDIENCE"))
	if domain == "" || clientID == "" || audience == "" {
		return nil, fmt.Errorf("auth is not configured: set SWEETRPG_AUTH_DOMAIN, SWEETRPG_AUTH_CLIENT_ID, and SWEETRPG_AUTH_AUDIENCE (or rebuild with -ldflags setting sweetrpg.auth domain, client id, and audience)")
	}
	return &Config{Domain: domain, ClientID: clientID, Audience: audience, Scopes: Scopes}, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func (c *Config) validate() error {
	switch {
	case c == nil:
		return errors.New("nil auth config")
	case c.Domain == "":
		return errors.New("auth config missing domain")
	case c.ClientID == "":
		return errors.New("auth config missing client id")
	}
	return nil
}

// DeviceCode is the start of one authorization attempt.
type DeviceCode struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"` // seconds
	Interval                int    `json:"interval"`   // seconds

	// startedAt anchors ExpiresIn so AwaitAuthorization can detect expiry;
	// pollFloor overrides the minimum interval in tests.
	startedAt time.Time
	pollFloor time.Duration
}

func (d *DeviceCode) interval() time.Duration {
	floor := d.floor()
	if d.Interval <= 0 {
		return floor
	}
	dur := time.Duration(d.Interval) * time.Second
	if dur < floor {
		return floor
	}
	return dur
}

func (d *DeviceCode) floor() time.Duration {
	if d.pollFloor > 0 {
		return d.pollFloor
	}
	return defaultPollFloor
}

func (d *DeviceCode) deadline() time.Time {
	return d.startedAt.Add(time.Duration(d.ExpiresIn) * time.Second)
}

// pollFloor/startedAt are test hooks set by RequestDeviceCode.
const defaultPollFloor = 5 * time.Second

// TokenResponse is a successful token endpoint reply. RefreshToken may be
// empty on refresh grants that do not rotate it.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	IDToken      string `json:"id_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
}

// PollStatus classifies one token-endpoint poll result.
type PollStatus int

const (
	StatusAuthorized PollStatus = iota
	StatusPending               // user has not approved yet - keep polling
	StatusSlowDown              // polling too fast - back off and keep polling
)

// ErrDenied and ErrExpired end the flow; every other error is transport or
// server trouble surfaced to the caller as-is.
var (
	ErrDenied  = errors.New("authorization denied")
	ErrExpired = errors.New("device code expired before authorization completed")
	ErrPending = errors.New("authorization pending")
)

// tokenError is an /oauth/token error payload.
type tokenError struct {
	Error       string `json:"error"`
	Description string `json:"error_description"`
}

// requestDeviceCode starts the device grant against cfg's tenant.
func requestDeviceCode(ctx context.Context, hc *http.Client, cfg *Config) (*DeviceCode, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	form := url.Values{
		"client_id": {cfg.ClientID},
		"scope":     {cfg.Scopes},
		"audience":  {cfg.Audience},
	}
	var dc DeviceCode
	if err := postForm(ctx, hc, cfg.deviceCodeURL(), form, &dc); err != nil {
		return nil, fmt.Errorf("requesting device code: %w", err)
	}
	if dc.DeviceCode == "" {
		return nil, fmt.Errorf("device code response missing device_code")
	}
	dc.startedAt = time.Now()
	return &dc, nil
}

// pollToken performs exactly one token poll. The returned status tells the
// caller whether to keep going; slowDownExtra reports how much longer to wait
// after a StatusSlowDown.
func pollToken(ctx context.Context, hc *http.Client, cfg *Config, dc *DeviceCode) (*TokenResponse, PollStatus, time.Duration, error) {
	form := url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"client_id":   {cfg.ClientID},
		"device_code": {dc.DeviceCode},
	}
	var out TokenResponse
	err := postForm(ctx, hc, cfg.tokenURL(), form, &out)
	if err == nil {
		if out.AccessToken == "" {
			return nil, 0, 0, fmt.Errorf("token response missing access_token")
		}
		return &out, StatusAuthorized, 0, nil
	}

	var te *endpointError
	if !errors.As(err, &te) {
		return nil, 0, 0, err
	}
	switch te.Code {
	case "authorization_pending":
		return nil, StatusPending, 0, nil
	case "slow_down":
		return nil, StatusSlowDown, slowDownExtra, nil
	case "access_denied":
		return nil, 0, 0, fmt.Errorf("%w: %s", ErrDenied, te.Description)
	case "expired_token", "invalid_grant":
		return nil, 0, 0, fmt.Errorf("%w (%s)", ErrExpired, te.Code)
	default:
		return nil, 0, 0, fmt.Errorf("token endpoint rejected poll: %s: %s", te.Code, te.Description)
	}
}

// slowDownExtra is the RFC 8628 penalty added to the interval on slow_down.
const slowDownExtra = 5 * time.Second

// AwaitAuthorization runs the polling loop until the user approves, denies,
// the code expires, or ctx is cancelled. sleep is called between polls with
// the delay to observe; production passes a context-aware sleeper.
func AwaitAuthorization(ctx context.Context, hc *http.Client, cfg *Config, dc *DeviceCode, sleep func(context.Context, time.Duration) error) (*TokenResponse, error) {
	delay := dc.interval()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		tok, status, extra, err := pollToken(ctx, hc, cfg, dc)
		if err != nil {
			return nil, err
		}
		switch status {
		case StatusAuthorized:
			return tok, nil
		case StatusSlowDown:
			delay += extra
		case StatusPending:
			// fall through to sleep
		}
		if time.Now().Add(delay).After(dc.deadline()) {
			return nil, ErrExpired
		}
		if err := sleep(ctx, delay); err != nil {
			return nil, err
		}
	}
}

func (c *Config) deviceCodeURL() string { return c.baseURL() + "/oauth/device/code" }
func (c *Config) tokenURL() string      { return c.baseURL() + "/oauth/token" }
func (c *Config) revokeURL() string     { return c.baseURL() + "/oauth/revoke" }

func (c *Config) baseURL() string {
	d := strings.TrimSuffix(c.Domain, "/")
	if strings.HasPrefix(d, "http://") || strings.HasPrefix(d, "https://") {
		return d
	}
	return "https://" + d
}
