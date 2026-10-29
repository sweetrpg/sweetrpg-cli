// Package entitlements loads an entitlements-api catalog seed through its admin API, the same
// path an administrator uses, so every row is validated and audited. The seed and price files are
// supplied by the caller (they live in the entitlements-api repo); this package holds no catalog
// data.
package entitlements

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Doc is a seed file. Stripe price IDs are left empty until prices exist in Stripe; set them
// through the admin API afterwards.
type Doc struct {
	Keys        []Key     `json:"keys"`
	DefaultTier string    `json:"default_tier"`
	Tiers       []Tier    `json:"tiers"`
	Products    []Product `json:"products"`
}

// Key mirrors the admin API's key body.
type Key struct {
	Key         string `json:"key"`
	Type        string `json:"type"`
	Description string `json:"description"`
	TTLSeconds  int    `json:"ttl_seconds"`
	Max         *int64 `json:"max,omitempty"`
}

// Tier mirrors the admin API's tier body.
type Tier struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	Description   string         `json:"description"`
	Values        map[string]any `json:"values"`
	StripePriceID string         `json:"stripe_price_id,omitempty"`
}

// Product mirrors the admin API's product body.
type Product struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	Description   string         `json:"description"`
	Billing       string         `json:"billing"`
	Repeatable    bool           `json:"repeatable,omitempty"`
	PlanGroup     string         `json:"plan_group,omitempty"`
	StripePriceID string         `json:"stripe_price_id,omitempty"`
	Entitlements  map[string]any `json:"entitlements"`
}

// Parse decodes a seed file, rejecting unknown fields so a typo cannot silently drop data.
func Parse(b []byte) (*Doc, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var d Doc
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("seed: %w", err)
	}
	return &d, nil
}

// Report says what a load did. Existing rows are left exactly as they are.
type Report struct {
	Created  []string
	Existing []string
	// Skipped rows were deliberately not changed.
	Skipped []string
	// Replaced lists live prices swapped for the overlay's (or, in a dry run, that would be),
	// each as "price <product> (<old> -> <new>)". Only populated with Client.ReplacePrices.
	Replaced []string
}

// Pacing and retry defaults. The service's standard rate limit is 30 requests per 60 seconds per
// client, and it answers 429 without a Retry-After header, so a rate-limited request waits out an
// exponential backoff that reaches a full window.
const (
	DefaultDelay      = 300 * time.Millisecond
	DefaultMaxRetries = 6
	baseBackoff       = 5 * time.Second
	maxBackoff        = 60 * time.Second
	maxRetryAfter     = 2 * time.Minute
)

// Client talks to a running entitlements-api admin API.
type Client struct {
	// BaseURL includes any ingress prefix, e.g. https://host/api/1/entitlements.
	BaseURL string
	// Token is an administrator's bearer token. It is only ever sent, never logged.
	Token string
	HTTP  *http.Client
	// ReplacePrices lets the overlay replace a product's existing, different price. Off by
	// default: replacing a live price is deliberate, so it is reported as skipped instead.
	ReplacePrices bool
	// Delay is the pause before each request after the first, to stay under the rate limit.
	Delay time.Duration
	// MaxRetries bounds how many times one request is retried after a 429.
	MaxRetries int
	// Logf receives progress notes such as a rate-limit wait. Nil discards them.
	Logf func(format string, args ...any)
	// Sleep waits for d or until ctx ends; nil uses a real timer. Tests replace it.
	Sleep func(ctx context.Context, d time.Duration) error

	sent bool
}

// state is what the service already holds, read once with a handful of list calls instead of one
// probe per row.
type state struct {
	keys     map[string]bool
	tiers    map[string]bool
	products map[string]liveProduct
	defTier  string
}

// Load creates what is missing: keys, tiers, the default tier (only when none is set), then
// products, each carrying its overlay price when it has one. An existing product that has no
// price gets the overlay price; one with a different price is left and reported as skipped. It
// never overwrites anything else, so re-running it cannot undo an administrator's edit. prices may
// be nil. DryRun reports without writing.
func (c *Client) Load(ctx context.Context, d *Doc, prices *Prices, dryRun bool) (*Report, error) {
	r := &Report{}
	st, err := c.readState(ctx)
	if err != nil {
		return r, err
	}
	for _, k := range d.Keys {
		if err := c.create(ctx, r, st.keys[k.Key], "key "+k.Key, "/admin/keys", k, dryRun); err != nil {
			return r, err
		}
	}
	for _, t := range d.Tiers {
		if err := c.create(ctx, r, st.tiers[t.ID], "tier "+t.ID, "/admin/tiers", t, dryRun); err != nil {
			return r, err
		}
	}
	if err := c.setDefaultTier(ctx, r, st.defTier, d.DefaultTier, dryRun); err != nil {
		return r, err
	}
	for _, p := range d.Products {
		if lp, ok := st.products[p.ID]; ok {
			if err := c.applyPrice(ctx, r, lp, prices.priceFor(p.ID), dryRun); err != nil {
				return r, err
			}
			continue
		}
		p.StripePriceID = prices.priceFor(p.ID)
		if err := c.create(ctx, r, false, "product "+p.ID, "/admin/products", p, dryRun); err != nil {
			return r, err
		}
	}
	return r, prices.checkKnown(d, st)
}

func (p *Prices) priceFor(id string) string {
	if p == nil {
		return ""
	}
	return p.Products[id]
}

// checkKnown rejects an overlay entry that names a product that is neither in the seed nor already
// live, which would otherwise be silently ignored.
func (p *Prices) checkKnown(d *Doc, st *state) error {
	if p == nil {
		return nil
	}
	seeded := map[string]bool{}
	for _, pr := range d.Products {
		seeded[pr.ID] = true
	}
	for id := range p.Products {
		if _, live := st.products[id]; !seeded[id] && !live {
			return fmt.Errorf("prices: product %s is not in the seed and does not exist", id)
		}
	}
	return nil
}

func (c *Client) readState(ctx context.Context) (*state, error) {
	st := &state{keys: map[string]bool{}, tiers: map[string]bool{}, products: map[string]liveProduct{}}
	var keys []struct {
		Key string `json:"key"`
	}
	if err := c.list(ctx, "/admin/keys", &keys); err != nil {
		return nil, err
	}
	for _, k := range keys {
		st.keys[k.Key] = true
	}
	var tiers []struct {
		ID string `json:"id"`
	}
	if err := c.list(ctx, "/admin/tiers", &tiers); err != nil {
		return nil, err
	}
	for _, t := range tiers {
		st.tiers[t.ID] = true
	}
	var products []liveProduct
	if err := c.list(ctx, "/admin/products", &products); err != nil {
		return nil, err
	}
	for _, p := range products {
		st.products[p.ID] = p
	}
	var def struct {
		ID string `json:"id"`
	}
	if err := c.getJSON(ctx, "/admin/default-tier", &def); err != nil {
		return nil, err
	}
	st.defTier = def.ID
	return st, nil
}

// listPage is the admin API's largest page.
const listPage = 200

// list reads every row of a collection, paging only if a page comes back full.
func (c *Client) list(ctx context.Context, path string, into any) error {
	var all []json.RawMessage
	for skip := 0; ; skip += listPage {
		var page []json.RawMessage
		if err := c.getJSON(ctx, fmt.Sprintf("%s?limit=%d&skip=%d", path, listPage, skip), &page); err != nil {
			return err
		}
		all = append(all, page...)
		if len(page) < listPage {
			break
		}
	}
	b, err := json.Marshal(all)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, into)
}

func (c *Client) getJSON(ctx context.Context, path string, into any) error {
	status, body, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	if status != http.StatusOK {
		return apiError("reading "+path, status, body)
	}
	if err := json.Unmarshal(body, into); err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	return nil
}

// create posts a row unless it already exists. A 409 from a row created since the state was read
// is also "exists".
func (c *Client) create(ctx context.Context, r *Report, exists bool, label, path string, body any, dryRun bool) error {
	switch {
	case exists:
		r.Existing = append(r.Existing, label)
		return nil
	case dryRun:
		r.Created = append(r.Created, label)
		return nil
	}
	status, resp, err := c.do(ctx, http.MethodPost, path, body)
	switch {
	case err != nil:
		return fmt.Errorf("seed %s: %w", label, err)
	case status == http.StatusCreated:
		r.Created = append(r.Created, label)
	case status == http.StatusConflict:
		r.Existing = append(r.Existing, label)
	default:
		return apiError("seed "+label, status, resp)
	}
	return nil
}

func (c *Client) setDefaultTier(ctx context.Context, r *Report, current, want string, dryRun bool) error {
	if want == "" {
		return nil
	}
	label := "default tier " + want
	if current != "" {
		r.Existing = append(r.Existing, label)
		return nil
	}
	if dryRun {
		r.Created = append(r.Created, label)
		return nil
	}
	status, resp, err := c.do(ctx, http.MethodPut, "/admin/default-tier", map[string]string{"id": want})
	if err != nil {
		return fmt.Errorf("seed %s: %w", label, err)
	}
	if status != http.StatusNoContent {
		return apiError("seed "+label, status, resp)
	}
	r.Created = append(r.Created, label)
	return nil
}

// applyPrice sets the overlay price on an existing product that has none. The same price is
// reported as existing; a different one is left alone and reported as skipped unless
// ReplacePrices is set, since replacing a live price is deliberate. Only the price changes: the
// rest of the product is sent back as the service returned it.
func (c *Client) applyPrice(ctx context.Context, r *Report, lp liveProduct, price string, dryRun bool) error {
	switch {
	case price == "":
		r.Existing = append(r.Existing, "product "+lp.ID)
		return nil
	case lp.StripePriceID == price:
		r.Existing = append(r.Existing, "product "+lp.ID, "price "+lp.ID)
		return nil
	case lp.StripePriceID != "" && !c.ReplacePrices:
		r.Existing = append(r.Existing, "product "+lp.ID)
		r.Skipped = append(r.Skipped, "price "+lp.ID+" (already set to a different price)")
		return nil
	}
	replacing := lp.StripePriceID != ""
	old := lp.StripePriceID
	if !dryRun {
		lp.StripePriceID = price
		status, resp, err := c.do(ctx, http.MethodPut, "/admin/products/"+url.PathEscape(lp.ID), lp)
		if err != nil {
			return fmt.Errorf("prices %s: %w", lp.ID, err)
		}
		if status != http.StatusOK {
			return apiError("prices "+lp.ID, status, resp)
		}
	}
	r.Existing = append(r.Existing, "product "+lp.ID)
	if replacing {
		r.Replaced = append(r.Replaced, fmt.Sprintf("price %s (%s -> %s)", lp.ID, old, price))
		return nil
	}
	r.Created = append(r.Created, "price "+lp.ID)
	return nil
}

// apiError describes an unexpected response with its body, which carries the service's message.
func apiError(what string, status int, body []byte) error {
	return fmt.Errorf("%s: status %d: %s", what, status, strings.TrimSpace(string(body)))
}

// do sends one request, pausing first (pacing) and retrying a 429 with backoff. It returns the
// final status and body; only transport failures and exhausted context are errors.
func (c *Client) do(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var payload []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		payload = b
	}
	for attempt := 0; ; attempt++ {
		if c.sent && c.Delay > 0 {
			if err := c.sleep(ctx, c.Delay); err != nil {
				return 0, nil, err
			}
		}
		c.sent = true
		status, header, out, err := c.once(ctx, method, path, payload)
		if err != nil || status != http.StatusTooManyRequests {
			return status, out, err
		}
		if attempt >= c.maxRetries() {
			return status, out, fmt.Errorf("rate limited after %d retries: %s", attempt, strings.TrimSpace(string(out)))
		}
		wait := retryWait(header, attempt, time.Now())
		c.logf("rate limited by the service; waiting %s before retry %d of %d", wait.Round(time.Second), attempt+1, c.maxRetries())
		if err := c.sleep(ctx, wait); err != nil {
			return 0, nil, err
		}
	}
}

func (c *Client) once(ctx context.Context, method, path string, payload []byte) (int, http.Header, []byte, error) {
	var rd io.Reader
	if payload != nil {
		rd = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, rd)
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	h := c.HTTP
	if h == nil {
		h = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := h.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, resp.Header, out, err
}

func (c *Client) maxRetries() int {
	if c.MaxRetries <= 0 {
		return DefaultMaxRetries
	}
	return c.MaxRetries
}

func (c *Client) logf(format string, args ...any) {
	if c.Logf != nil {
		c.Logf(format, args...)
	}
}

func (c *Client) sleep(ctx context.Context, d time.Duration) error {
	if c.Sleep != nil {
		return c.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// retryWait chooses how long to wait after a 429: Retry-After (seconds or an HTTP date) if sent,
// else the time until X-RateLimit-Reset (epoch seconds), else exponential backoff by attempt.
// The result is never shorter than a second or longer than maxRetryAfter.
func retryWait(h http.Header, attempt int, now time.Time) time.Duration {
	wait := backoff(attempt)
	if v := strings.TrimSpace(h.Get("Retry-After")); v != "" {
		if secs, err := strconv.Atoi(v); err == nil {
			wait = time.Duration(secs) * time.Second
		} else if at, err := http.ParseTime(v); err == nil {
			wait = at.Sub(now)
		}
	} else if v := strings.TrimSpace(h.Get("X-RateLimit-Reset")); v != "" {
		if epoch, err := strconv.ParseInt(v, 10, 64); err == nil {
			wait = time.Unix(epoch, 0).Sub(now)
		}
	}
	return min(max(wait, time.Second), maxRetryAfter)
}

func backoff(attempt int) time.Duration {
	d := baseBackoff << min(attempt, 10)
	return min(d, maxBackoff)
}

// Prices is a per-environment overlay mapping product IDs to Stripe price IDs. Price IDs differ
// between Stripe test and live mode, so they live outside the seed.
type Prices struct {
	Products map[string]string `json:"products"`
}

// ParsePrices decodes an overlay, rejecting unknown fields and empty prices. A price ID is an
// opaque string: Stripe generates price_... values, but one can be set by hand (members_10).
func ParsePrices(b []byte) (*Prices, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var p Prices
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("prices: %w", err)
	}
	for id, price := range p.Products {
		if price == "" || strings.TrimSpace(price) != price {
			return nil, fmt.Errorf("prices: %s: price ID must be a non-empty string without surrounding space", id)
		}
	}
	return &p, nil
}

// liveProduct is a product as the admin API returns it; Entitlements stays raw so it round-trips
// exactly (including at_least objects).
type liveProduct struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Description   string          `json:"description"`
	Billing       string          `json:"billing"`
	Repeatable    bool            `json:"repeatable"`
	PlanGroup     string          `json:"plan_group,omitempty"`
	StripePriceID string          `json:"stripe_price_id,omitempty"`
	Entitlements  json.RawMessage `json:"entitlements"`
}
