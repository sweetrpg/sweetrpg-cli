package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// endpointError is a structured OAuth error payload ({error,
// error_description}) returned by Auth0 endpoints. Code carries the OAuth
// error name ("authorization_pending", "slow_down", ...).
type endpointError struct {
	StatusCode  int
	Code        string
	Description string
}

func (e *endpointError) Error() string {
	if e.Description == "" {
		return fmt.Sprintf("auth endpoint returned %d (%s)", e.StatusCode, e.Code)
	}
	return fmt.Sprintf("auth endpoint returned %d %s: %s", e.StatusCode, e.Code, e.Description)
}

// postForm sends one form-urlencoded request and decodes the JSON reply into
// out. Non-2xx replies become *endpointError.
func postForm(ctx context.Context, hc *http.Client, endpoint string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20)); _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("reading auth response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		te := &endpointError{StatusCode: resp.StatusCode}
		var parsed struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		if json.Unmarshal(body, &parsed) == nil && parsed.Error != "" {
			te.Code = parsed.Error
			te.Description = parsed.Description
		} else {
			te.Code = "unparseable_response"
			te.Description = strings.TrimSpace(string(body))
		}
		return te
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decoding auth response: %w", err)
	}
	return nil
}

// SleepContext is the production sleeper: waits d or until ctx is done.
func SleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
