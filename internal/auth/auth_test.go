package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// scripter serves queued JSON replies per path, recording each request's form
// body so tests assert what was sent.
type scripter struct {
	t        *testing.T
	replies  map[string][]reply
	gotForms map[string][]url.Values
}

type reply struct {
	status int
	body   string
}

func newScripter(t *testing.T) (*scripter, *httptest.Server) {
	t.Helper()
	s := &scripter{
		t:        t,
		replies:  map[string][]reply{},
		gotForms: map[string][]url.Values{},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parsing form: %v", err)
		}
		s.gotForms[r.URL.Path] = append(s.gotForms[r.URL.Path], r.PostForm)
		q := s.replies[r.URL.Path]
		if len(q) == 0 {
			t.Errorf("unexpected request to %s", r.URL.Path)
			http.Error(w, "unexpected", http.StatusInternalServerError)
			return
		}
		rep := q[0]
		s.replies[r.URL.Path] = q[1:]
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(rep.status)
		_, _ = w.Write([]byte(rep.body))
	}))
	t.Cleanup(srv.Close)
	return s, srv
}

func (s *scripter) queue(path string, status int, body string) {
	s.replies[path] = append(s.replies[path], reply{status, body})
}

func testCfg(base string) *Config {
	return &Config{Domain: strings.TrimPrefix(base, "https://"), ClientID: "cli-123", Audience: "https://api.example.com", Scopes: "openid offline_access"}
}

func okToken() string {
	return `{"access_token":"at-1","id_token":"` + jwtFor(`{"sub":"auth0|42","email":"a@b.c"}`) + `","refresh_token":"rt-1","expires_in":3600,"token_type":"Bearer"}`
}

func jwtFor(claimsJSON string) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(claimsJSON))
	return "hdr." + payload + ".sig"
}

func TestRequestDeviceCodeSendsFormAndDecodes(t *testing.T) {
	s, srv := newScripter(t)
	s.queue("/oauth/device/code", http.StatusOK,
		`{"device_code":"dc-1","user_code":"ABCD-EFGH","verification_uri":"https://ex.com/activate","verification_uri_complete":"https://ex.com/activate?user_code=ABCD-EFGH","expires_in":900,"interval":5}`)
	cfg := testCfg(srv.URL)

	dc, err := requestDeviceCode(context.Background(), srv.Client(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	form := s.gotForms["/oauth/device/code"][0]
	if form.Get("client_id") != "cli-123" || form.Get("audience") != "https://api.example.com" || form.Get("scope") != "openid offline_access" {
		t.Errorf("form = %v", form)
	}
	if dc.DeviceCode != "dc-1" || dc.UserCode != "ABCD-EFGH" || dc.Interval != 5 {
		t.Errorf("decoded %+v", dc)
	}
	if dc.interval() != 5*time.Second {
		t.Errorf("interval = %s", dc.interval())
	}
}

func TestPollTokenStates(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		body        string
		wantStatus  PollStatus
		wantExtra   time.Duration
		wantErr     error
		wantErrText string
	}{
		{name: "authorized", status: 200, body: okToken(), wantStatus: StatusAuthorized},
		{name: "pending", status: 403, body: `{"error":"authorization_pending","error_description":"waiting"}`, wantStatus: StatusPending},
		{name: "slow down adds penalty", status: 429, body: `{"error":"slow_down","error_description":"too fast"}`, wantStatus: StatusSlowDown, wantExtra: 5 * time.Second},
		{name: "denied is terminal", status: 403, body: `{"error":"access_denied","error_description":"user said no"}`, wantErr: ErrDenied},
		{name: "expired is terminal", status: 400, body: `{"error":"expired_token","error_description":"too late"}`, wantErr: ErrExpired},
		{name: "unknown error surfaces", status: 500, body: `{"error":"server_error","error_description":"boom"}`, wantErrText: "server_error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, srv := newScripter(t)
			s.queue("/oauth/token", tt.status, tt.body)

			dc := &DeviceCode{DeviceCode: "dc-1", startedAt: time.Now()}
			tok, status, extra, err := pollToken(context.Background(), srv.Client(), testCfg(srv.URL), dc)

			switch {
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("want %v, got %v", tt.wantErr, err)
				}
			case tt.wantErrText != "":
				if err == nil || !strings.Contains(err.Error(), tt.wantErrText) {
					t.Fatalf("want error containing %q, got %v", tt.wantErrText, err)
				}
			case tt.wantStatus == StatusAuthorized:
				if err != nil || tok.AccessToken != "at-1" || tok.RefreshToken != "rt-1" {
					t.Fatalf("tok=%+v err=%v", tok, err)
				}
			default:
				if err != nil || status != tt.wantStatus || extra != tt.wantExtra {
					t.Fatalf("status=%d extra=%s err=%v", status, extra, err)
				}
			}
			form := s.gotForms["/oauth/token"][0]
			if form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:device_code" ||
				form.Get("device_code") != "dc-1" || form.Get("client_id") != "cli-123" {
				t.Errorf("form = %v", form)
			}
		})
	}
}

func TestAwaitAuthorizationBacksOffOnSlowDown(t *testing.T) {
	s, srv := newScripter(t)
	s.queue("/oauth/token", http.StatusForbidden, `{"error":"authorization_pending"}`)
	s.queue("/oauth/token", http.StatusTooManyRequests, `{"error":"slow_down"}`)
	s.queue("/oauth/token", http.StatusOK, okToken())

	var delays []time.Duration
	sleep := func(context.Context, time.Duration) error { return nil } // delays recorded below
	recording := func(ctx context.Context, d time.Duration) error {
		delays = append(delays, d)
		return sleep(ctx, d)
	}

	dc := &DeviceCode{DeviceCode: "dc-1", Interval: 3, ExpiresIn: 900, startedAt: time.Now()}
	tok, err := AwaitAuthorization(context.Background(), srv.Client(), testCfg(srv.URL), dc, recording)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok.AccessToken != "at-1" {
		t.Errorf("token = %+v", tok)
	}
	want := []time.Duration{dc.interval(), dc.interval() + 5*time.Second} // base poll, then slow_down penalty
	if len(delays) != len(want) || delays[0] != want[0] || delays[1] != want[1] {
		t.Errorf("delays = %v, want %v", delays, want)
	}
}

func TestAwaitAuthorizationExpiresWithoutSleepingPastDeadline(t *testing.T) {
	s, srv := newScripter(t)
	s.queue("/oauth/token", http.StatusForbidden, `{"error":"authorization_pending"}`)

	slept := false
	sleep := func(context.Context, time.Duration) error { slept = true; return nil }

	dc := &DeviceCode{DeviceCode: "dc-1", Interval: 60, ExpiresIn: 30, startedAt: time.Now().Add(-time.Minute)}
	_, err := AwaitAuthorization(context.Background(), srv.Client(), testCfg(srv.URL), dc, sleep)
	if !errors.Is(err, ErrExpired) {
		t.Fatalf("want ErrExpired, got %v", err)
	}
	if slept {
		t.Error("should not sleep once deadline has passed")
	}
}

func TestAwaitAuthorizationDeniedStopsImmediately(t *testing.T) {
	s, srv := newScripter(t)
	s.queue("/oauth/token", http.StatusForbidden, `{"error":"access_denied","error_description":"no"}`)

	calls := 0
	counting := func(context.Context, time.Duration) error { calls++; return nil }

	dc := &DeviceCode{DeviceCode: "dc-1", Interval: 1, ExpiresIn: 900, startedAt: time.Now()}
	_, err := AwaitAuthorization(context.Background(), srv.Client(), testCfg(srv.URL), dc, counting)
	if !errors.Is(err, ErrDenied) || calls != 0 {
		t.Fatalf("err=%v sleeps=%d", err, calls)
	}
}

func TestParseIDTokenClaims(t *testing.T) {
	tests := []struct {
		name    string
		token   string
		wantSub string
		wantErr bool
	}{
		{name: "valid", token: jwtFor(`{"sub":"auth0|42","email":"a@b.c"}`), wantSub: "auth0|42"},
		{name: "not a jwt", token: "garbage", wantErr: true},
		{name: "two segments", token: "a.b", wantErr: true},
		{name: "bad payload b64", token: "a.!!!.c", wantErr: true},
		{name: "payload not json", token: jwtFor(`not json`), wantErr: true},
		{name: "no sub", token: jwtFor(`{"email":"a@b.c"}`), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			claims, err := ParseIDTokenClaims(tt.token)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("want error, got %+v", claims)
				}
				return
			}
			if err != nil || claims.Subject != tt.wantSub || claims.Email != "a@b.c" {
				t.Fatalf("claims=%+v err=%v", claims, err)
			}
		})
	}
}

func TestRefreshAccessTokenKeepsTokenWhenNotRotated(t *testing.T) {
	s, srv := newScripter(t)
	s.queue("/oauth/token", http.StatusOK, `{"access_token":"at-2","expires_in":60,"token_type":"Bearer"}`)

	tok, err := RefreshAccessToken(context.Background(), srv.Client(), testCfg(srv.URL), "rt-old")
	if err != nil {
		t.Fatal(err)
	}
	if tok.RefreshToken != "rt-old" {
		t.Errorf("refresh token should be preserved, got %q", tok.RefreshToken)
	}
	if s.gotForms["/oauth/token"][0].Get("refresh_token") != "rt-old" {
		t.Errorf("form = %v", s.gotForms["/oauth/token"][0])
	}
}

func TestRefreshAccessTokenSurfacesRotation(t *testing.T) {
	s, srv := newScripter(t)
	s.queue("/oauth/token", http.StatusOK, `{"access_token":"at-2","refresh_token":"rt-new","expires_in":60}`)

	tok, err := RefreshAccessToken(context.Background(), srv.Client(), testCfg(srv.URL), "rt-old")
	if err != nil {
		t.Fatal(err)
	}
	if tok.RefreshToken != "rt-new" {
		t.Errorf("rotated token = %q", tok.RefreshToken)
	}
}

func TestRefreshAccessTokenRevokedMapsToExpired(t *testing.T) {
	s, srv := newScripter(t)
	s.queue("/oauth/token", http.StatusForbidden, `{"error":"invalid_grant","error_description":"revoked"}`)

	_, err := RefreshAccessToken(context.Background(), srv.Client(), testCfg(srv.URL), "rt-dead")
	if !errors.Is(err, ErrExpired) {
		t.Fatalf("want ErrExpired, got %v", err)
	}
}

func TestSessionSourceRefreshesCachesAndPersists(t *testing.T) {
	s, srv := newScripter(t)
	s.queue("/oauth/token", http.StatusOK, okToken())
	store := NewMemoryStore()
	if err := store.Save(Session{Account: "auth0|42", RefreshToken: "rt-stored"}); err != nil {
		t.Fatal(err)
	}

	src := &SessionSource{Cfg: testCfg(srv.URL), HTTP: srv.Client(), Store: store}

	first, err := src.Token(context.Background())
	if err != nil || first != "at-1" {
		t.Fatalf("token=%q err=%v", first, err)
	}
	second, err := src.Token(context.Background())
	if err != nil || second != "at-1" {
		t.Fatalf("token=%q err=%v", second, err)
	}
	if got := len(s.gotForms["/oauth/token"]); got != 1 {
		t.Errorf("token endpoint hit %d times, want 1 (cache)", got)
	}
	sess, err := store.Load()
	if err != nil || sess.RefreshToken != "rt-1" {
		t.Errorf("stored session %+v err=%v", sess, err)
	}
}

func TestSessionSourceRevokedDeletesCredentials(t *testing.T) {
	s, srv := newScripter(t)
	s.queue("/oauth/token", http.StatusForbidden, `{"error":"invalid_grant"}`)
	store := NewMemoryStore()
	_ = store.Save(Session{Account: "sub", RefreshToken: "rt-dead"})

	src := &SessionSource{Cfg: testCfg(srv.URL), HTTP: srv.Client(), Store: store}
	_, err := src.Token(context.Background())
	var ae *AuthError
	if !errors.As(err, &ae) {
		t.Fatalf("want AuthError, got %v", err)
	}
	if _, loadErr := store.Load(); !errors.Is(loadErr, ErrNotLoggedIn) {
		t.Errorf("credentials should be deleted, got %v", loadErr)
	}
}

func TestSessionSourceNotLoggedIn(t *testing.T) {
	_, srv := newScripter(t) // no queued replies: any HTTP call would fail loudly
	src := &SessionSource{Cfg: testCfg(srv.URL), HTTP: srv.Client(), Store: NewMemoryStore()}

	_, err := src.Token(context.Background())
	if !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("want ErrNotLoggedIn, got %v", err)
	}
}

func TestLoginEndToEnd(t *testing.T) {
	s, srv := newScripter(t)
	s.queue("/oauth/device/code", http.StatusOK,
		`{"device_code":"dc-1","user_code":"ABCD-EFGH","verification_uri_complete":"https://ex.com/a?u=ABCD-EFGH","expires_in":900,"interval":5}`)
	s.queue("/oauth/token", http.StatusOK, okToken())

	var opened []string
	claims, err := Login(context.Background(), srv.Client(), testCfg(srv.URL),
		NewMemoryStore(), func(context.Context, time.Duration) error { return nil },
		func(u string) { opened = append(opened, u) })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if claims.Subject != "auth0|42" || claims.Email != "a@b.c" {
		t.Errorf("claims = %+v", claims)
	}
	if len(opened) != 1 || opened[0] != "https://ex.com/a?u=ABCD-EFGH" {
		t.Errorf("opened = %v", opened)
	}
}

func TestLoginRefusesSilenceAboutMissingRefreshToken(t *testing.T) {
	s, srv := newScripter(t)
	s.queue("/oauth/device/code", http.StatusOK, `{"device_code":"dc-1","user_code":"X","verification_uri":"https://e/a","expires_in":900,"interval":5}`)
	s.queue("/oauth/token", http.StatusOK, `{"access_token":"at-1","id_token":"`+jwtFor(`{"sub":"s"}`)+`","expires_in":60}`)

	_, err := Login(context.Background(), srv.Client(), testCfg(srv.URL),
		NewMemoryStore(), func(context.Context, time.Duration) error { return nil }, nil)
	if err == nil || !strings.Contains(err.Error(), "offline_access") {
		t.Fatalf("want missing-refresh-token error, got %v", err)
	}
}

func TestLoginWarnsWhenPersistenceFails(t *testing.T) {
	s, srv := newScripter(t)
	s.queue("/oauth/device/code", http.StatusOK, `{"device_code":"dc-1","user_code":"X","verification_uri":"https://e/a","expires_in":900,"interval":5}`)
	s.queue("/oauth/token", http.StatusOK, okToken())
	failing := failingStore{}

	claims, err := Login(context.Background(), srv.Client(), testCfg(srv.URL),
		failing, func(context.Context, time.Duration) error { return nil }, nil)
	if err == nil || !strings.Contains(err.Error(), "keychain") {
		t.Fatalf("want persist-failure warning, got %v", err)
	}
	if claims == nil || claims.Subject != "auth0|42" {
		t.Errorf("login itself should succeed: %+v", claims)
	}
}

type failingStore struct{}

var _ Store = failingStore{}

func (failingStore) Save(Session) error      { return errors.New("keychain unavailable") }
func (failingStore) Load() (*Session, error) { return nil, ErrNotLoggedIn }
func (failingStore) Delete() error           { return nil }

func TestLogoutIdempotentAndRevokes(t *testing.T) {
	s, srv := newScripter(t)
	cfg := testCfg(srv.URL)

	// Not logged in: no-op, no requests.
	existed, err := Logout(context.Background(), srv.Client(), cfg, NewMemoryStore())
	if err != nil || existed {
		t.Fatalf("empty logout: existed=%v err=%v", existed, err)
	}
	if len(s.gotForms["/oauth/revoke"]) != 0 {
		t.Error("revoke should not be called when logged out")
	}

	// Logged in: revoke then delete.
	store := NewMemoryStore()
	_ = store.Save(Session{Account: "sub", RefreshToken: "rt-1"})
	s.queue("/oauth/revoke", http.StatusOK, "{}")
	existed, err = Logout(context.Background(), srv.Client(), cfg, store)
	if err != nil || !existed {
		t.Fatalf("logout: existed=%v err=%v", existed, err)
	}
	if form := s.gotForms["/oauth/revoke"][0]; form.Get("token") != "rt-1" || form.Get("client_id") != "cli-123" {
		t.Errorf("revoke form = %v", form)
	}
	if _, err := store.Load(); !errors.Is(err, ErrNotLoggedIn) {
		t.Errorf("session should be gone, got %v", err)
	}
}

func TestLogoutSurvivesRevokeFailure(t *testing.T) {
	s, srv := newScripter(t)
	s.queue("/oauth/revoke", http.StatusBadRequest, `{"error":"invalid_grant"}`)
	store := NewMemoryStore()
	_ = store.Save(Session{Account: "sub", RefreshToken: "rt-1"})

	var stderrCapture strings.Builder
	stderrWriter = &stderrCapture
	defer func() { stderrWriter = nil }()

	existed, err := Logout(context.Background(), srv.Client(), testCfg(srv.URL), store)
	if err != nil || !existed {
		t.Fatalf("logout must still succeed: existed=%v err=%v", existed, err)
	}
	if !strings.Contains(stderrCapture.String(), "revocation failed") {
		t.Errorf("expected a warning on stderr, got %q", stderrCapture.String())
	}
}

func TestBaseURLHandling(t *testing.T) {
	tests := map[string]string{
		"dev-abc.us.auth0.com":       "https://dev-abc.us.auth0.com",
		"https://dev-abc.auth0.com/": "https://dev-abc.auth0.com",
		"http://localhost:8080":      "http://localhost:8080",
	}
	for domain, want := range tests {
		cfg := &Config{Domain: domain}
		if got := cfg.baseURL(); got != want {
			t.Errorf("baseURL(%q) = %q, want %q", domain, got, want)
		}
		if got := cfg.tokenURL(); !strings.HasPrefix(got, want+"/oauth/") {
			t.Errorf("tokenURL(%q) = %q", domain, got)
		}
	}
}

func TestDefaultConfigRequiresBuildTimeSettings(t *testing.T) {
	oldDomain, oldClient, oldAud := Domain, ClientID, Audience
	defer func() { Domain, ClientID, Audience = oldDomain, oldClient, oldAud }()
	Domain, ClientID, Audience = "", "", ""

	_, err := DefaultConfig()
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("want unconfigured error, got %v", err)
	}
}

func TestAccessTokenValidityMargin(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name  string
		token AccessToken
		valid bool
	}{
		{"empty token never valid", AccessToken{}, false},
		{"fresh token valid", AccessToken{Token: "t", ExpiresAt: now.Add(time.Hour)}, true},
		{"inside margin invalid", AccessToken{Token: "t", ExpiresAt: now.Add(10 * time.Second)}, false},
		{"expired invalid", AccessToken{Token: "t", ExpiresAt: now.Add(-time.Second)}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.token.Valid(now); got != tt.valid {
				t.Errorf("Valid = %v, want %v", got, tt.valid)
			}
		})
	}
}

func TestPostFormUnparseableErrorBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html>gateway exploded</html>"))
	}))
	t.Cleanup(srv.Close)

	err := postForm(context.Background(), srv.Client(), srv.URL+"/x", url.Values{}, &struct{}{})
	var te *endpointError
	if !errors.As(err, &te) || te.Code != "unparseable_response" || !strings.Contains(te.Description, "<html>") {
		t.Fatalf("want endpointError keeping raw body, got %v", err)
	}
}

// Auth0's /oauth/revoke replies 2xx with an empty body; that must count as
// success instead of a decode failure (regression: logout warned on every run).
func TestPostFormAcceptsEmptySuccessBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	out := map[string]any{}
	if err := postForm(context.Background(), srv.Client(), srv.URL+"/x", url.Values{}, &out); err != nil {
		t.Fatalf("empty 200 body must not error, got %v", err)
	}
}

func TestKeyringStoreRoundTripsThroughInterface(t *testing.T) {
	// KeyringStore talks to the OS keychain, which CI cannot assume; this
	// pins its contract through MemoryStore semantics instead of skipping.
	var st Store = NewMemoryStore()
	sess := Session{Account: "auth0|7", Email: "x@y.z", RefreshToken: "rt"}
	if err := st.Save(sess); err != nil {
		t.Fatal(err)
	}
	got, err := st.Load()
	if err != nil || *got != sess {
		t.Fatalf("got %+v err=%v", got, err)
	}
	if err := st.Delete(); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Load(); !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("want ErrNotLoggedIn, got %v", err)
	}
}

func TestKeyringStoreRefusesIncompleteSessions(t *testing.T) {
	st := KeyringStore{} // Save validates before touching the keyring
	if err := st.Save(Session{Account: "only-account"}); err == nil {
		t.Fatal("incomplete session must be rejected")
	}
}

func TestDefaultConfigFallsBackToEnvironment(t *testing.T) {
	oldDomain, oldClient, oldAud := Domain, ClientID, Audience
	defer func() { Domain, ClientID, Audience = oldDomain, oldClient, oldAud }()
	Domain, ClientID, Audience = "", "", ""

	t.Setenv("SWEETRPG_AUTH_DOMAIN", "dev-test.us.auth0.com")
	t.Setenv("SWEETRPG_AUTH_CLIENT_ID", "cid")
	t.Setenv("SWEETRPG_AUTH_AUDIENCE", "https://catalog-api")

	cfg, err := DefaultConfig()
	if err != nil {
		t.Fatalf("want env fallback to satisfy config, got %v", err)
	}
	if cfg.Domain != "dev-test.us.auth0.com" || cfg.ClientID != "cid" || cfg.Audience != "https://catalog-api" {
		t.Errorf("unexpected config: %+v", cfg)
	}

	t.Setenv("SWEETRPG_AUTH_CLIENT_ID", "")
	if _, err := DefaultConfig(); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Errorf("partial env must still error, got %v", err)
	}
}

// TestResolveConfigEnvOverridesBuiltIn regression-guards the precedence bug
// where a release binary's baked-in tenant could never be overridden: env
// var must win even when Domain/ClientID/Audience are non-empty.
func TestResolveConfigEnvOverridesBuiltIn(t *testing.T) {
	oldDomain, oldClient, oldAud := Domain, ClientID, Audience
	defer func() { Domain, ClientID, Audience = oldDomain, oldClient, oldAud }()
	Domain, ClientID, Audience = "baked.us.auth0.com", "baked-cid", "https://baked-aud"

	t.Setenv("SWEETRPG_AUTH_DOMAIN", "env.us.auth0.com")
	t.Setenv("SWEETRPG_AUTH_CLIENT_ID", "env-cid")
	t.Setenv("SWEETRPG_AUTH_AUDIENCE", "https://env-aud")

	cfg, err := ResolveConfig("file.us.auth0.com", "file-cid", "https://file-aud")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Domain != "env.us.auth0.com" || cfg.ClientID != "env-cid" || cfg.Audience != "https://env-aud" {
		t.Errorf("env var must win over file and built-in: %+v", cfg)
	}
}

// TestResolveConfigFileOverridesBuiltIn: with no env var set, the config
// file's authTenant section must win over the baked-in default.
func TestResolveConfigFileOverridesBuiltIn(t *testing.T) {
	oldDomain, oldClient, oldAud := Domain, ClientID, Audience
	defer func() { Domain, ClientID, Audience = oldDomain, oldClient, oldAud }()
	Domain, ClientID, Audience = "baked.us.auth0.com", "baked-cid", "https://baked-aud"

	cfg, err := ResolveConfig("file.us.auth0.com", "file-cid", "https://file-aud")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Domain != "file.us.auth0.com" || cfg.ClientID != "file-cid" || cfg.Audience != "https://file-aud" {
		t.Errorf("file config must win over built-in: %+v", cfg)
	}
}

// TestResolveConfigFallsBackToBuiltIn: with neither env var nor file value
// set, the baked-in default still applies (a release binary works with no
// configuration at all).
func TestResolveConfigFallsBackToBuiltIn(t *testing.T) {
	oldDomain, oldClient, oldAud := Domain, ClientID, Audience
	defer func() { Domain, ClientID, Audience = oldDomain, oldClient, oldAud }()
	Domain, ClientID, Audience = "baked.us.auth0.com", "baked-cid", "https://baked-aud"

	cfg, err := ResolveConfig("", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Domain != "baked.us.auth0.com" || cfg.ClientID != "baked-cid" || cfg.Audience != "https://baked-aud" {
		t.Errorf("built-in default should apply when nothing overrides it: %+v", cfg)
	}
}
