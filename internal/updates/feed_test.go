package updates

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeGitHub serves a releases listing and its release.json assets; the
// SERVER placeholder in asset links becomes the server's own URL.
func fakeGitHub(t *testing.T, releases string, metadata map[string]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var serverURL string
	mux.HandleFunc("/releases", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "30", r.URL.Query().Get("per_page"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(strings.ReplaceAll(releases, "SERVER", serverURL)))
	})
	mux.HandleFunc("/assets/", func(w http.ResponseWriter, r *http.Request) {
		body, ok := metadata[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	})
	server := httptest.NewServer(mux)
	serverURL = server.URL
	t.Cleanup(server.Close)
	return server
}

func TestGitHubFeedPicksNewestByVersionPerChannel(t *testing.T) {
	t.Parallel()
	server := fakeGitHub(t, `[
		{"tag_name":"v0.3.0-rc.1","prerelease":true,"published_at":"2026-09-01T00:00:00Z","html_url":"h/rc",
		 "assets":[{"name":"release.json","browser_download_url":"SERVER/assets/rc"}]},
		{"tag_name":"v0.2.1","prerelease":false,"published_at":"2026-08-20T00:00:00Z","html_url":"h/021",
		 "assets":[{"name":"checksums.txt","browser_download_url":"SERVER/assets/sums"}]},
		{"tag_name":"v0.2.2","prerelease":false,"draft":true,"published_at":"2026-08-25T00:00:00Z","html_url":"h/draft"},
		{"tag_name":"v0.10.0","prerelease":false,"published_at":"2026-08-01T00:00:00Z","html_url":"h/010",
		 "assets":[{"name":"release.json","browser_download_url":"SERVER/assets/010"}]},
		{"tag_name":"nightly","prerelease":false,"published_at":"2026-09-02T00:00:00Z","html_url":"h/nightly"}
	]`, map[string]string{
		"/assets/rc":  `{"version":"v0.3.0-rc.1","k3s":"v1.37.0+k3s1"}`,
		"/assets/010": `{"version":"v0.10.0","k3s":"v1.36.3+k3s1"}`,
	})
	feed := &GitHubFeed{URL: server.URL + "/releases", Client: server.Client()}
	ctx := context.Background()

	stable, err := feed.Latest(ctx, ChannelStable)
	require.NoError(t, err)
	require.Equal(t, "v0.10.0", stable.Version, "newest by version order, drafts and untagged builds ignored")
	require.False(t, stable.Prerelease)
	require.Equal(t, "v1.36.3+k3s1", stable.K3s)
	require.Equal(t, "h/010", stable.URL)

	beta, err := feed.Latest(ctx, ChannelBeta)
	require.NoError(t, err)
	require.Equal(t, "v0.10.0", beta.Version, "a prerelease of an older line never outranks a release")

	empty := fakeGitHub(t, `[]`, nil)
	none, err := (&GitHubFeed{URL: empty.URL + "/releases", Client: empty.Client()}).Latest(ctx, ChannelStable)
	require.NoError(t, err)
	require.Nil(t, none)

	_, err = (&GitHubFeed{URL: empty.URL + "/missing", Client: empty.Client()}).Latest(ctx, ChannelStable)
	require.ErrorContains(t, err, "HTTP 404")
}

func TestGitHubFeedBetaIncludesPrereleases(t *testing.T) {
	t.Parallel()
	server := fakeGitHub(t, `[
		{"tag_name":"v0.3.0-rc.1","prerelease":true,"published_at":"2026-09-01T00:00:00Z","html_url":"h/rc"},
		{"tag_name":"v0.2.1","prerelease":false,"published_at":"2026-08-20T00:00:00Z","html_url":"h/021"}
	]`, nil)
	feed := &GitHubFeed{URL: server.URL + "/releases", Client: server.Client()}
	stable, err := feed.Latest(context.Background(), ChannelStable)
	require.NoError(t, err)
	require.Equal(t, "v0.2.1", stable.Version)
	beta, err := feed.Latest(context.Background(), ChannelBeta)
	require.NoError(t, err)
	require.Equal(t, "v0.3.0-rc.1", beta.Version)
	require.True(t, beta.Prerelease)

	_, err = ParseChannel("nightly")
	require.Error(t, err)
}
