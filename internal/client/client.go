// Package client is a thin JSON:API client for catalog-api, typed against
// catalog-objects.go value objects. It covers list/get/create/patch/delete for
// every entity type plus the /search endpoints used for name resolution.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// MediaType is the JSON:API content type catalog-api serves.
const MediaType = "application/vnd.api+json"

// TokenSource supplies the bearer token per request. Returning an empty string
// sends no Authorization header.
type TokenSource func(ctx context.Context) (string, error)

// Client talks to one catalog-api deployment.
type Client struct {
	BaseURL *url.URL
	HTTP    *http.Client
	Tokens  TokenSource
}

// New parses baseURL (must be absolute http(s)) and returns a Client using the
// default HTTP transport.
func New(baseURL string, tokens TokenSource) (*Client, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("invalid catalog API base URL %q", baseURL)
	}
	return &Client{BaseURL: u, HTTP: http.DefaultClient, Tokens: tokens}, nil
}

// APIError carries a non-2xx response's status and parsed body. Body shapes
// vary by handler ({error,message} or {error}); anything unparseable keeps the
// raw text in Message so operators see what the server said.
type APIError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("catalog-api returned %d (%s)", e.StatusCode, e.Code)
	}
	return fmt.Sprintf("catalog-api returned %d %s: %s", e.StatusCode, e.Code, e.Message)
}

// IsNotFound reports whether err is a 404 from the server.
func IsNotFound(err error) bool {
	apiErr, ok := err.(*APIError)
	return ok && apiErr.StatusCode == http.StatusNotFound
}

// IsAuthError reports whether err indicates missing/insufficient credentials.
func IsAuthError(err error) bool {
	apiErr, ok := err.(*APIError)
	if !ok {
		return false
	}
	return apiErr.StatusCode == http.StatusUnauthorized || apiErr.StatusCode == http.StatusForbidden
}

func (c *Client) buildURL(plural, pathSuffix string, query url.Values) *url.URL {
	u := *c.BaseURL
	u.Path = strings.TrimRight(u.Path, "/") + "/" + plural + pathSuffix
	if len(query) > 0 {
		u.RawQuery = query.Encode()
	}
	return &u
}

func (c *Client) do(ctx context.Context, method string, u *url.URL, body io.Reader, contentType string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", MediaType)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if c.Tokens != nil {
		token, err := c.Tokens(ctx)
		if err != nil {
			return nil, fmt.Errorf("resolving access token: %w", err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		return nil, apiErrorFrom(resp)
	}
	return resp, nil
}

func apiErrorFrom(resp *http.Response) error {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return &APIError{StatusCode: resp.StatusCode, Message: "unreadable body"}
	}
	var parsed struct {
		Error   any    `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &parsed) == nil {
		code := ""
		switch v := parsed.Error.(type) {
		case string:
			code = v
		case nil:
		default:
			code = fmt.Sprint(v)
		}
		return &APIError{StatusCode: resp.StatusCode, Code: code, Message: parsed.Message}
	}
	return &APIError{StatusCode: resp.StatusCode, Message: strings.TrimSpace(string(raw))}
}

func sendJSON[T any](v T) (*bytes.Reader, error) {
	payload, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(payload), nil
}
