package client

import (
	"context"
	"fmt"
	"io"
	"net/http"
)

// CodeCLINotServed is the error code a cluster answers when it does not
// serve its CLI: the operator turned serving off, or the image ships no
// binaries (working-tree builds). The message names the required version.
const CodeCLINotServed = "cli_not_served"

// maxCLIBytes bounds a CLI download; the binaries are a few dozen
// megabytes, so a larger response is not one.
const maxCLIBytes = 256 << 20

// CLIListing is what a cluster serves at /v1/system/cli: its version and
// the platforms it can hand out, each with the digest the download is
// verified against.
type CLIListing struct {
	Version   string     `json:"version"`
	Enabled   bool       `json:"enabled"`
	Reason    string     `json:"reason,omitempty"`
	Platforms []CLIAsset `json:"platforms"`
}

// CLIAsset is one served binary.
type CLIAsset struct {
	Platform string `json:"platform"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
}

// Platform finds one platform in the listing.
func (l *CLIListing) Platform(platform string) (CLIAsset, bool) {
	for _, asset := range l.Platforms {
		if asset.Platform == platform {
			return asset, true
		}
	}
	return CLIAsset{}, false
}

// CLIPlatform names a build the way the cluster lists it: <goos>_<goarch>.
func CLIPlatform(goos, goarch string) string {
	return goos + "_" + goarch
}

// CLIListing reads the cluster's CLI listing.
func (c *Client) CLIListing(ctx context.Context) (*CLIListing, error) {
	var listing CLIListing
	if err := c.do(ctx, http.MethodGet, "/v1/system/cli", nil, &listing); err != nil {
		return nil, err
	}
	return &listing, nil
}

// DownloadCLI fetches one platform's skali binary from the cluster. The
// bytes come back unverified: the caller compares them against the digest
// the listing named. The streaming client is used because a binary on a
// slow link outlives the JSON timeout.
func (c *Client) DownloadCLI(ctx context.Context, platform string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/v1/system/cli/"+platform, nil)
	if err != nil {
		return nil, fmt.Errorf("client: %w", err)
	}
	c.stamp(req.Header)
	res, err := c.streaming.Do(req)
	if err != nil {
		return nil, fmt.Errorf("client: %s unreachable: %w", c.base, err)
	}
	defer res.Body.Close()
	if err := c.checkInstance(res); err != nil {
		return nil, err
	}
	if res.StatusCode >= 400 {
		raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		if err != nil {
			return nil, fmt.Errorf("client: read response: %w", err)
		}
		return nil, decodeErrorEnvelope(res.StatusCode, raw)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxCLIBytes+1))
	if err != nil {
		return nil, fmt.Errorf("client: read %s: %w", platform, err)
	}
	if len(body) > maxCLIBytes {
		return nil, fmt.Errorf("client: %s exceeds %d bytes", platform, maxCLIBytes)
	}
	return body, nil
}
