package client

import (
	"context"

	"github.com/Hinkolas/skali/internal/updates"
)

func (c *Client) UpdateStatus(ctx context.Context) (*updates.Status, error) {
	var result updates.Status
	err := c.do(ctx, "GET", "/v1/system/updates", nil, &result)
	return &result, err
}

func (c *Client) ScanUpdates(ctx context.Context) (*updates.Status, error) {
	var result updates.Status
	err := c.do(ctx, "POST", "/v1/system/updates/scan", nil, &result)
	return &result, err
}

func (c *Client) ApplyUpdate(ctx context.Context, target string) (*updates.Status, error) {
	var result updates.Status
	err := c.do(ctx, "POST", "/v1/system/updates/apply", map[string]string{"version": target}, &result)
	return &result, err
}

func (c *Client) ResumeUpdate(ctx context.Context) (*updates.Status, error) {
	var result updates.Status
	err := c.do(ctx, "POST", "/v1/system/updates/resume", nil, &result)
	return &result, err
}
