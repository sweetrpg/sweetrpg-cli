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
	"slices"
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
}

// Client talks to a running entitlements-api admin API.
type Client struct {
	// BaseURL includes any ingress prefix, e.g. https://host/api/1/entitlements.
	BaseURL string
	// Token is an administrator's bearer token. It is only ever sent, never logged.
	Token string
	HTTP  *http.Client
}

// Load creates what is missing: keys, tiers, the default tier (only when none is set), then
// products. It never overwrites an existing row, so re-running it cannot undo an administrator's
// edit or clear a Stripe price ID added after the first load. DryRun reports without writing.
func (c *Client) Load(ctx context.Context, d *Doc, dryRun bool) (*Report, error) {
	r := &Report{}
	for _, k := range d.Keys {
		if err := c.create(ctx, r, "key "+k.Key, "/admin/keys", k, dryRun); err != nil {
			return r, err
		}
	}
	for _, t := range d.Tiers {
		if err := c.create(ctx, r, "tier "+t.ID, "/admin/tiers", t, dryRun); err != nil {
			return r, err
		}
	}
	if err := c.setDefaultTier(ctx, r, d.DefaultTier, dryRun); err != nil {
		return r, err
	}
	for _, p := range d.Products {
		if err := c.create(ctx, r, "product "+p.ID, "/admin/products", p, dryRun); err != nil {
			return r, err
		}
	}
	return r, nil
}

func (c *Client) create(ctx context.Context, r *Report, label, path string, body any, dryRun bool) error {
	if dryRun {
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
		return fmt.Errorf("seed %s: status %d: %s", label, status, strings.TrimSpace(string(resp)))
	}
	return nil
}

func (c *Client) setDefaultTier(ctx context.Context, r *Report, id string, dryRun bool) error {
	if id == "" {
		return nil
	}
	label := "default tier " + id
	status, resp, err := c.do(ctx, http.MethodGet, "/admin/default-tier", nil)
	if err != nil || status != http.StatusOK {
		return fmt.Errorf("seed %s: reading current default: status %d err %v", label, status, err)
	}
	var cur struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(resp, &cur); err != nil {
		return fmt.Errorf("seed %s: %w", label, err)
	}
	if cur.ID != "" {
		r.Existing = append(r.Existing, label)
		return nil
	}
	if dryRun {
		r.Created = append(r.Created, label)
		return nil
	}
	status, resp, err = c.do(ctx, http.MethodPut, "/admin/default-tier", map[string]string{"id": id})
	if err != nil || status != http.StatusNoContent {
		return fmt.Errorf("seed %s: status %d: %s err %v", label, status, strings.TrimSpace(string(resp)), err)
	}
	r.Created = append(r.Created, label)
	return nil
}

func (c *Client) do(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, rd)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	h := c.HTTP
	if h == nil {
		h = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := h.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, out, err
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

// ApplyPrices sets each overlay price on its product when the product has none. It works on
// products that already exist, which Load never touches. A product that already has the same
// price is reported as such; one with a different price is left alone and reported, since
// replacing a live price is a deliberate act, not something a seed re-run should do.
func (c *Client) ApplyPrices(ctx context.Context, p *Prices, dryRun bool, r *Report) error {
	status, body, err := c.do(ctx, http.MethodGet, "/admin/products?limit=200", nil)
	if err != nil || status != http.StatusOK {
		return fmt.Errorf("prices: listing products: status %d err %v", status, err)
	}
	var live []liveProduct
	if err := json.Unmarshal(body, &live); err != nil {
		return fmt.Errorf("prices: %w", err)
	}
	byID := make(map[string]liveProduct, len(live))
	for _, lp := range live {
		byID[lp.ID] = lp
	}
	ids := make([]string, 0, len(p.Products))
	for id := range p.Products {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		if err := c.applyPrice(ctx, r, byID, id, p.Products[id], dryRun); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) applyPrice(ctx context.Context, r *Report, byID map[string]liveProduct, id, price string, dryRun bool) error {
	lp, ok := byID[id]
	switch {
	case !ok && dryRun:
		r.Created = append(r.Created, "price "+id+" (product not created yet)")
		return nil
	case !ok:
		return fmt.Errorf("prices: product %s does not exist; load the seed first", id)
	case lp.StripePriceID == price:
		r.Existing = append(r.Existing, "price "+id)
		return nil
	case lp.StripePriceID != "":
		r.Skipped = append(r.Skipped, "price "+id+" (already set to a different price)")
		return nil
	}
	if !dryRun {
		lp.StripePriceID = price
		status, resp, err := c.do(ctx, http.MethodPut, "/admin/products/"+id, lp)
		if err != nil || status != http.StatusOK {
			return fmt.Errorf("prices: %s: status %d: %s err %v", id, status, strings.TrimSpace(string(resp)), err)
		}
	}
	r.Created = append(r.Created, "price "+id)
	return nil
}
