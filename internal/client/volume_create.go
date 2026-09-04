package client

import (
	"context"
	"fmt"

	jsonapi "github.com/google/jsonapi"
	"github.com/sweetrpg/catalog-objects.go/vo"
	modelcore "github.com/sweetrpg/model-core.go/vo"
)

// VolumeCreateFields is the POST /volumes wire body. It is deliberately not
// vo.VolumeVO: catalog-api's create endpoint (matching PATCH /volumes/:id)
// takes tags as plain names ([]string), not the {name,value} TagVO shape
// GET/list responses return - posting a VolumeVO's Tags field as-is 400s with
// "cannot unmarshal object into ... of type string".
type VolumeCreateFields struct {
	Title        string                 `json:"title"`
	Description  string                 `json:"description,omitempty"`
	Notes        string                 `json:"notes,omitempty"`
	Format       string                 `json:"format,omitempty"`
	Tags         []string               `json:"tags,omitempty"`
	Properties   []modelcore.PropertyVO `json:"properties,omitempty"`
	PublisherIDs []string               `json:"publisherIds,omitempty"`
}

// CreateVolume posts fields to POST /volumes and returns the created, live
// volume record.
func CreateVolume(ctx context.Context, c *Client, fields VolumeCreateFields) (*vo.VolumeVO, error) {
	body, err := sendJSON(fields)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(ctx, "POST", c.buildURL("volumes", "", nil), body, "application/json")
	if err != nil {
		return nil, err
	}
	defer drainAndClose(resp)

	var out vo.VolumeVO
	if err := jsonapi.UnmarshalPayload(resp.Body, &out); err != nil {
		return nil, fmt.Errorf("decoding created volume: %w", err)
	}
	return &out, nil
}
