package dtrpg

import (
	"net/http"
	"sync"
)

// userAgent identifies this CLI to DriveThruRPG. Neither dtrpg-sdk.go request
// path lets a caller inject a client or a header - auth.Authenticate uses
// http.DefaultClient, and library.Client's own *http.Client is unconfigured,
// so both fall back to Go's zero-value transport. Go's default
// "Go-http-client/x.y" User-Agent trips DriveThruRPG's edge WAF: it returns a
// plain-text 503 ("Blocked for not following robot.txt rules, bad robot")
// that isn't JSON, so the SDK's own decode fails with a confusing error
// instead of surfacing the real cause. A descriptive User-Agent passes
// through cleanly - confirmed directly against api.drivethrurpg.com.
const userAgent = "sweetrpg-cli (+https://github.com/sweetrpg/sweetrpg-cli)"

var installUserAgentOnce sync.Once

// installUserAgentTransport wraps the process's default RoundTripper so every
// request that falls back to it (both dtrpg-sdk.go paths) carries a
// User-Agent. Idempotent and safe to call before every DriveThruRPG call;
// nothing else in this CLI uses http.DefaultClient or an unconfigured
// *http.Client, so the wrapping is scoped to DriveThruRPG traffic in practice.
func installUserAgentTransport() {
	installUserAgentOnce.Do(func() {
		http.DefaultTransport = &userAgentTransport{base: http.DefaultTransport}
	})
}

type userAgentTransport struct {
	base http.RoundTripper
}

func (t *userAgentTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Header.Get("User-Agent") == "" {
		req = req.Clone(req.Context())
		req.Header.Set("User-Agent", userAgent)
	}
	return t.base.RoundTrip(req)
}
