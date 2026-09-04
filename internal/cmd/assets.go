package cmd

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/sweetrpg/sweetrpg-cli/internal/auth"
	"github.com/sweetrpg/sweetrpg-cli/internal/client"
	"github.com/sweetrpg/sweetrpg-cli/internal/config"
)

// buildAssetsClient is a var so tests can point uploads at a fixture server.
var buildAssetsClient = func() (*client.AssetsClient, error) { return newAssetsClient() }

// newAssetsClient wires the same keychain-backed session the catalog client
// uses into an assets-web client. Uploads always require a session; there is
// no anonymous fallback.
func newAssetsClient() (*client.AssetsClient, error) {
	cfg, err := config.Load(config.Sources{
		FlagAssetsWebURL: flagAssetsWebURL,
		Getenv:           os.Getenv,
		HomeDir:          os.UserHomeDir,
	})
	if err != nil {
		return nil, err
	}
	authCfg, err := auth.DefaultConfig()
	if err != nil {
		return nil, fmt.Errorf("asset upload requires a session: %w", err)
	}
	source := &auth.SessionSource{Cfg: authCfg, HTTP: &http.Client{Timeout: 30 * time.Second}, Store: auth.KeyringStore{}}
	return client.NewAssetsClient(cfg.AssetsWebURL, tokenFunc(source, true))
}

// assetFlag extends one entity's edit command with a file-upload flag. The
// apply closure runs after name resolution with the resolved record ID.
type assetFlag struct {
	name    string
	usage   string
	require bool // counts toward "nothing to update" even when no field flags are set
	apply   func(ctx context.Context, c *client.Client, id string, path string) (*client.WriteDisposition, error)
}

// assetFlags holds the per-entity upload flags. Only volumes carry assets
// today; add entries here as other types grow them.
var assetFlags = map[string]assetFlag{
	"volume": {
		name:    "cover",
		usage:   "replace the volume cover with this image (png/jpg/webp)",
		require: true,
		apply: func(ctx context.Context, c *client.Client, id string, path string) (*client.WriteDisposition, error) {
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("--cover: %w", err)
			}
			contentType, err := client.ContentTypeForFilename(path)
			if err != nil {
				return nil, fmt.Errorf("--cover: %w", err)
			}
			assets, err := buildAssetsClient()
			if err != nil {
				return nil, err
			}
			_, disp, err := client.SetVolumeCover(ctx, c, assets, id, data, contentType)
			if err != nil {
				return nil, err
			}
			return &disp, nil
		},
	},
}

// lookupAssetFlag returns the entity's upload flag definition, if any.
func lookupAssetFlag(entityType string) (assetFlag, bool) {
	af, ok := assetFlags[entityType]
	return af, ok && af.apply != nil
}
