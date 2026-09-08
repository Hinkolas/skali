// Package updates is the product side of platform updates: it learns what
// releases exist, remembers the operator's channel and auto-update choice,
// judges whether the cluster can move, and asks the installer-owned
// coordinator to move it. It never touches a host itself: the only mutation
// it performs is writing a desired version into the cluster state, and the
// coordinator and every node's hostd decide how to get there.
package updates

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Hinkolas/skali/internal/version"
)

// Channel selects which releases count. Stable follows tagged releases;
// beta also follows prereleases (alpha, beta, rc tags), which GitHub marks
// as such and goreleaser never points "latest" at.
type Channel string

const (
	ChannelStable Channel = "stable"
	ChannelBeta   Channel = "beta"
)

// ParseChannel accepts the two known channels and nothing else.
func ParseChannel(value string) (Channel, error) {
	switch Channel(value) {
	case ChannelStable, ChannelBeta:
		return Channel(value), nil
	}
	return "", fmt.Errorf("unknown update channel %q", value)
}

// Release is one published skali release as the feed reports it.
type Release struct {
	Version     string    `json:"version"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
	// URL is the human release page (notes, assets).
	URL string `json:"url,omitempty"`
	// K3s is the k3s pin the release's installer carries, from its
	// release.json; empty when the release predates that asset.
	K3s string `json:"k3s,omitempty"`
}

// Feed answers "what is the newest release on this channel". Nil, nil
// means the channel has no release yet.
type Feed interface {
	Latest(ctx context.Context, channel Channel) (*Release, error)
}

// GitHubFeed reads the releases API of the skali repository. Drafts never
// count; prereleases count on the beta channel only. The newest release
// wins by version order, not by publish date, so a patch for an older line
// never masquerades as the latest.
type GitHubFeed struct {
	// URL is the releases listing, https://api.github.com/repos/<repo>/releases.
	URL    string
	Client *http.Client
}

// DefaultFeedURL is the GitHub releases API for the release repository.
const DefaultFeedURL = "https://api.github.com/repos/" + version.ReleaseRepo + "/releases"

type githubRelease struct {
	TagName     string    `json:"tag_name"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
	HTMLURL     string    `json:"html_url"`
	Assets      []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

func (f *GitHubFeed) client() *http.Client {
	if f.Client != nil {
		return f.Client
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func (f *GitHubFeed) Latest(ctx context.Context, channel Channel) (*Release, error) {
	url := f.URL
	if url == "" {
		url = DefaultFeedURL
	}
	body, err := f.get(ctx, url+"?per_page=30", 4<<20)
	if err != nil {
		return nil, err
	}
	var listed []githubRelease
	if err := json.Unmarshal(body, &listed); err != nil {
		return nil, fmt.Errorf("decode release feed: %w", err)
	}
	var best *githubRelease
	for index := range listed {
		candidate := &listed[index]
		if candidate.Draft || !version.IsRelease(candidate.TagName) {
			continue
		}
		if candidate.Prerelease && channel != ChannelBeta {
			continue
		}
		if best == nil || version.Older(best.TagName, candidate.TagName) {
			best = candidate
		}
	}
	if best == nil {
		return nil, nil
	}
	release := &Release{
		Version: best.TagName, Prerelease: best.Prerelease,
		PublishedAt: best.PublishedAt, URL: best.HTMLURL,
	}
	for _, asset := range best.Assets {
		if asset.Name != "release.json" {
			continue
		}
		metadata, err := f.get(ctx, asset.URL, 64<<10)
		if err != nil {
			// The pin is informational; a missing or unreadable file must
			// not hide the release itself.
			break
		}
		var decoded struct {
			K3s string `json:"k3s"`
		}
		if json.Unmarshal(metadata, &decoded) == nil {
			release.K3s = decoded.K3s
		}
		break
	}
	return release, nil
}

func (f *GitHubFeed) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "skalid/"+version.Version)
	response, err := f.client().Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", url, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s: HTTP %d", url, response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", url, err)
	}
	if int64(len(body)) > limit {
		return nil, errors.New("release feed response is too large")
	}
	return body, nil
}
