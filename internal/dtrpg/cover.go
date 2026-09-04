package dtrpg

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// maxCoverBytes caps the download so a misbehaving response can't exhaust
// memory on a bulk run.
const maxCoverBytes = 20 << 20 // 20MB

// allowedCoverTypes mirrors assets-web's upload allowlist (client.Upload's doc
// comment): a cover in any other format is a skip, not an upload attempt.
var allowedCoverTypes = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/webp": true,
}

// Cover is a downloaded cover image ready to upload.
type Cover struct {
	Data        []byte
	ContentType string
}

// FetchCover downloads a product's cover image from DriveThruRPG. Every
// failure mode (no URL, network error, non-2xx, oversized body, unsupported
// content type) returns a nil *Cover and a descriptive error - callers treat
// this as a per-product warning, never a fatal one.
func FetchCover(ctx context.Context, httpClient *http.Client, url string) (*Cover, error) {
	if strings.TrimSpace(url) == "" {
		return nil, fmt.Errorf("no cover image for this product")
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("cover fetch failed: %s returned %d", url, resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxCoverBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxCoverBytes {
		return nil, fmt.Errorf("cover image exceeds %d bytes", maxCoverBytes)
	}

	contentType := resp.Header.Get("Content-Type")
	if idx := strings.Index(contentType, ";"); idx >= 0 {
		contentType = contentType[:idx]
	}
	contentType = strings.TrimSpace(contentType)
	if contentType == "" {
		contentType = http.DetectContentType(data)
	}
	if !allowedCoverTypes[contentType] {
		return nil, fmt.Errorf("unsupported cover content type %q (supported: png, jpeg, webp)", contentType)
	}

	return &Cover{Data: data, ContentType: contentType}, nil
}
