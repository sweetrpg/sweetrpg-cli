package client

import (
	"bytes"
	"context"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"path/filepath"
	"strings"
)

// Asset kinds the CLI writes. These are the live kinds: catalog-api's
// finalize-session flow promotes staged assets to cover/<volumeID> and
// sample/<volumeID>-<n>, so uploading straight to the live kind plus a PATCH
// produces the same end state without an edit session (sessions have no REST
// API - they are written directly to Redis by catalog-api/catalog-web).
const (
	AssetKindCover  = "cover"
	AssetKindSample = "sample"
)

// AssetsClient talks to one assets-web deployment.
type AssetsClient struct {
	BaseURL *url.URL
	HTTP    *http.Client
	Tokens  TokenSource
}

// NewAssetsClient parses baseURL (must be absolute http(s)) and returns an
// AssetsClient using the default HTTP transport.
func NewAssetsClient(baseURL string, tokens TokenSource) (*AssetsClient, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("invalid assets web base URL %q", baseURL)
	}
	return &AssetsClient{BaseURL: u, HTTP: http.DefaultClient, Tokens: tokens}, nil
}

func (c *AssetsClient) assetURL(kind, id string) *url.URL {
	u := *c.BaseURL
	u.Path = strings.TrimRight(u.Path, "/") + "/asset/" + kind + "/" + id
	return &u
}

// Upload stores data at assets-web's /asset/<kind>/<id>. The wire format is a
// multipart form with one "file" part carrying the image bytes; the server
// requires a non-empty filename and an image/png, image/jpeg, or image/webp
// content type. Success is 201 Created.
func (c *AssetsClient) Upload(ctx context.Context, kind, id string, data []byte, contentType string) error {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	// assets-web only needs a non-empty filename (it renames to image.<ext>);
	// the id keeps stored parts identifiable. The per-part Content-Type is what
	// the server validates against its allowlist.
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, id))
	if contentType != "" {
		header.Set("Content-Type", contentType)
	}
	part, err := writer.CreatePart(header)
	if err != nil {
		return fmt.Errorf("assets: build multipart form: %w", err)
	}
	if _, err := part.Write(data); err != nil {
		return fmt.Errorf("assets: write multipart body: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("assets: close multipart form: %w", err)
	}

	resp, err := sendRequest(ctx, c.BaseURL, c.HTTP, c.Tokens, "assets-web",
		http.MethodPost, c.assetURL(kind, id), &buf, writer.FormDataContentType())
	if err != nil {
		return err
	}
	drainAndClose(resp)
	return nil
}

// ContentTypeForFilename maps an image file extension to the MIME type
// assets-web accepts, or an error naming the supported types.
func ContentTypeForFilename(name string) (string, error) {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png":
		return "image/png", nil
	case ".jpg", ".jpeg":
		return "image/jpeg", nil
	case ".webp":
		return "image/webp", nil
	default:
		return "", fmt.Errorf("unsupported image type %q: supported types are png, jpeg, webp", filepath.Ext(name))
	}
}
