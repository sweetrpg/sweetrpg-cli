package client

import (
	"context"
	"fmt"

	"github.com/sweetrpg/catalog-objects.go/vo"
)

// maxVolumeSamples mirrors catalog-api's cap; failing here saves a round trip
// and keeps the error message identical in shape.
const maxVolumeSamples = 5

// SetVolumeCover uploads image as the volume's live cover asset
// (cover/<volumeID> on assets-web) and links it via PATCH. Admins/editors get
// a live write back; submitters get a submitted change (the server rejects
// coverAssetId from submitter roles, surfacing that as an *APIError).
func SetVolumeCover(ctx context.Context, c *Client, assets *AssetsClient, volumeID string, image []byte, contentType string) (*vo.VolumeVO, WriteDisposition, error) {
	if err := assets.Upload(ctx, AssetKindCover, volumeID, image, contentType); err != nil {
		return nil, WriteDisposition{}, fmt.Errorf("uploading cover: %w", err)
	}
	return Patch[vo.VolumeVO](ctx, c, "volumes", volumeID, map[string]any{"coverAssetId": volumeID})
}

// SetVolumeSamples uploads each image as sample/<volumeID>-<i> (order
// preserved) and replaces the volume's sample list in one PATCH. An empty
// slice clears all samples.
func SetVolumeSamples(ctx context.Context, c *Client, assets *AssetsClient, volumeID string, samples [][]byte, contentType string) (*vo.VolumeVO, WriteDisposition, error) {
	if len(samples) > maxVolumeSamples {
		return nil, WriteDisposition{}, fmt.Errorf("a volume may have at most %d samples, got %d", maxVolumeSamples, len(samples))
	}
	ids := make([]string, len(samples))
	for i, image := range samples {
		id := fmt.Sprintf("%s-%d", volumeID, i)
		if err := assets.Upload(ctx, AssetKindSample, id, image, contentType); err != nil {
			return nil, WriteDisposition{}, fmt.Errorf("uploading sample %d: %w", i, err)
		}
		ids[i] = id
	}
	return Patch[vo.VolumeVO](ctx, c, "volumes", volumeID, map[string]any{"sampleAssetIds": ids})
}
