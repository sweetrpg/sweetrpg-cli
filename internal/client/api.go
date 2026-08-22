package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"

	jsonapi "github.com/google/jsonapi"
)

// Filter is one equality filter: filter[field]=v1,v2 on the wire. catalog-api
// maps these to Mongo $eq/$in queries.
type Filter struct {
	Field  string
	Values []string
}

// ListOptions narrows a list request.
type ListOptions struct {
	Filters []Filter
	Start   int
	Limit   int
}

func (o ListOptions) query() url.Values {
	q := url.Values{}
	for _, f := range o.Filters {
		q.Set(fmt.Sprintf("filter[%s]", f.Field), joinValues(f.Values))
	}
	if o.Start > 0 {
		q.Set("page[start]", fmt.Sprint(o.Start))
	}
	if o.Limit > 0 {
		q.Set("page[limit]", fmt.Sprint(o.Limit))
	}
	return q
}

func joinValues(values []string) string {
	out := ""
	for i, v := range values {
		if i > 0 {
			out += ","
		}
		out += v
	}
	return out
}

// WriteDisposition reports how the server treated a patch: applied live, or
// recorded as a proposed change awaiting review.
type WriteDisposition struct {
	Submitted bool
	Version   int
	State     string
	Message   string
}

// Get fetches one entity by ID into out (a *vo.X). A 404 yields an *APIError
// with StatusCode 404 (see IsNotFound).
func Get[T any](ctx context.Context, c *Client, plural, id string) (*T, error) {
	resp, err := c.do(ctx, "GET", c.buildURL(plural, "/"+id, nil), nil, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out T
	if err := jsonapi.UnmarshalPayload(resp.Body, &out); err != nil {
		return nil, fmt.Errorf("decoding %s %s: %w", plural, id, err)
	}
	return &out, nil
}

// decodeList reads a JSON:API collection document into a typed slice. The
// result is never nil; an empty page is an empty slice.
func decodeList[T any](resp *http.Response, what string) ([]*T, error) {
	models, err := jsonapi.UnmarshalManyPayload(resp.Body, reflect.TypeOf((*T)(nil)))
	if err != nil {
		return nil, fmt.Errorf("decoding %s: %w", what, err)
	}
	out := make([]*T, 0, len(models))
	for _, m := range models {
		out = append(out, m.(*T))
	}
	return out, nil
}

// List fetches entities matching opts. The result is never nil; an empty page
// is an empty slice.
func List[T any](ctx context.Context, c *Client, plural string, opts ListOptions) ([]*T, error) {
	resp, err := c.do(ctx, "GET", c.buildURL(plural, "", opts.query()), nil, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return decodeList[T](resp, plural+" list")
}

// Search hits the type's /search endpoint. Only Searchable entity types
// support it.
func Search[T any](ctx context.Context, c *Client, plural, q string) ([]*T, error) {
	query := url.Values{"q": {q}}
	resp, err := c.do(ctx, "GET", c.buildURL(plural, "/search", query), nil, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return decodeList[T](resp, plural+" search")
}

// Create posts the entity as plain JSON (catalog-api binds VOs directly, not
// as JSON:API requests) and returns the created record from the JSON:API
// response document.
func Create[T any](ctx context.Context, c *Client, plural string, entity *T) (*T, error) {
	body, err := sendJSON(entity)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(ctx, "POST", c.buildURL(plural, "", nil), body, "application/json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out T
	if err := jsonapi.UnmarshalPayload(resp.Body, &out); err != nil {
		return nil, fmt.Errorf("decoding created %s: %w", plural, err)
	}
	return &out, nil
}

// Patch sends flat field updates ({name: "...", tags: [...]}) and returns the
// live record plus how the server disposed of the change. When disposition.
// Submitted is true the live record is nil and Version/State/Message describe
// the proposed change.
func Patch[T any](ctx context.Context, c *Client, plural, id string, fields map[string]any) (*T, WriteDisposition, error) {
	body, err := sendJSON(fields)
	if err != nil {
		return nil, WriteDisposition{}, err
	}
	resp, err := c.do(ctx, "PATCH", c.buildURL(plural, "/"+id, nil), body, "application/json")
	if err != nil {
		return nil, WriteDisposition{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 202 {
		var submitted struct {
			Version int    `json:"version"`
			State   string `json:"state"`
			Message string `json:"message"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&submitted); err != nil {
			return nil, WriteDisposition{}, fmt.Errorf("decoding submission response: %w", err)
		}
		return nil, WriteDisposition{Submitted: true, Version: submitted.Version, State: submitted.State, Message: submitted.Message}, nil
	}

	var out T
	if err := jsonapi.UnmarshalPayload(resp.Body, &out); err != nil {
		return nil, WriteDisposition{}, fmt.Errorf("decoding patched %s: %w", plural, err)
	}
	return &out, WriteDisposition{}, nil
}

// Delete issues the soft-delete request. Success is 204 No Content.
func Delete(ctx context.Context, c *Client, plural, id string) error {
	resp, err := c.do(ctx, "DELETE", c.buildURL(plural, "/"+id, nil), nil, "")
	if err != nil {
		return err
	}
	return resp.Body.Close()
}
