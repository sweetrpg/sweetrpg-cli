package dtrpg

import (
	"context"
	"errors"
	"time"

	"github.com/pilgrimagesoftware/dtrpg-sdk.go/library"
)

// maxRateLimitRetries bounds Retry-After honoring so a server stuck returning
// 429 can't hang the import forever.
const maxRateLimitRetries = 5

// sleepFn is time.Sleep, indirected so tests don't wait out real Retry-After
// intervals.
var sleepFn = func(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Library is the owner's full DriveThruRPG library: every ordered product
// across all pages, plus the sideloaded Publisher/Product resources they
// reference.
type Library struct {
	Products []library.OrderProductItem
	Included []library.IncludedItem
}

// FetchLibrary pages through the authenticated user's order_products,
// requesting filter categories so tags can be mapped. Rate-limit responses
// carrying Retry-After are waited out before retrying the same page.
//
// The archived query parameter is deliberately left unset. An earlier
// version unconditionally passed archived=true on the (SDK-doc-comment)
// assumption that it additively includes archived items alongside active
// ones; against a real ~1700-title account that returned only 40 - evidently
// an archived-only filter, not "everything". Leaving the parameter off
// returns the account's full, unfiltered order_products, and each item's own
// Archived field (already read downstream in mapping.go) still drives
// per-product archived/active classification - no server-side filtering
// needed for that at all.
//
// onPage, when non-nil, is called after each page is fetched with the page
// number just retrieved and the running product total - callers use it for
// progress output; pass nil to skip it.
func (s *Session) FetchLibrary(ctx context.Context, pageSize uint32, onPage func(page int, fetched int)) (Library, error) {
	var out Library
	yes := true
	page := uint32(1)

	for {
		params := library.LibraryItemsParams{
			Page:       &page,
			GetFilters: &yes,
		}
		if pageSize > 0 {
			params.PageSize = &pageSize
		}

		resp, err := s.listPageWithRetry(ctx, params)
		if err != nil {
			return Library{}, err
		}

		out.Products = append(out.Products, resp.Data...)
		out.Included = append(out.Included, resp.Included...)
		if onPage != nil {
			onPage(int(page), len(out.Products))
		}

		if len(resp.Data) == 0 || resp.Links.Next == nil {
			return out, nil
		}
		page++
	}
}

// listPageWithRetry issues one ListOrderProducts call, retrying after the
// server's Retry-After delay on rate-limit responses.
func (s *Session) listPageWithRetry(ctx context.Context, params library.LibraryItemsParams) (library.OrderProductListResponse, error) {
	for attempt := 0; ; attempt++ {
		resp, err := s.lib.ListOrderProducts(ctx, params)
		if err == nil {
			return resp, nil
		}

		var apiErr *library.APIError
		if attempt < maxRateLimitRetries && errors.As(err, &apiErr) && apiErr.RetryAfter != nil {
			if sleepErr := sleepFn(ctx, *apiErr.RetryAfter); sleepErr != nil {
				return library.OrderProductListResponse{}, sleepErr
			}
			continue
		}
		return library.OrderProductListResponse{}, err
	}
}
