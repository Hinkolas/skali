package installer

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Curl is routed through an in-process fixture server; install and mkdir are
// redirected so the real bootstrap script never touches system directories.
func TestInstallScript(t *testing.T) {
	type release struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
		Body       string `json:"body"`
		Assets     any    `json:"assets,omitempty"`
	}
	// Deliberately put names before IDs and include nested lookalikes.
	assetMetadata := json.RawMessage(`[{"name":"skali_linux_amd64","uploader":{"name":"skali_linux_amd64","id":999},"id":1},{"name":"skali-hostd_linux_amd64","id":2},{"name":"checksums.txt","id":3}]`)
	candidates := []release{
		{Tag: "v0.1.99"},
		{Tag: "v0.3.0-rc.2", Prerelease: true},
		{Tag: "v0.3.0-rc.10", Prerelease: true, Body: `Fake metadata: {"tag_name":"v99.0.0","draft":false} and an escaped slash \\"`},
		{Tag: "v9.0.0", Draft: true},
		{Tag: "v99.0.0-dev"},
		{Tag: "v0.3.0-beta.99", Prerelease: true},
	}
	for _, tc := range []struct {
		name, channel, pin, want, failure, raw string
		releases                               []release
		private, pretty, badChecksum, noStable bool
	}{
		{name: "stable default", want: "v0.2.0"},
		{name: "beta includes prereleases", channel: "beta", releases: candidates, want: "v0.3.0-rc.10"},
		{name: "pretty metadata", channel: "beta", releases: candidates, want: "v0.3.0-rc.10", pretty: true},
		{name: "stable outranks its prereleases", channel: "beta", releases: append(append([]release{}, candidates...), release{Tag: "v0.3.0"}), want: "v0.3.0"},
		{name: "version overrides channel", pin: "v0.1.0-alpha.1", want: "v0.1.0-alpha.1"},
		{name: "legacy latest alias", channel: "beta", pin: "latest", releases: candidates, want: "v0.3.0-rc.10"},
		{name: "private asset IDs", channel: "beta", releases: candidates, want: "v0.3.0-rc.10", private: true},
		{name: "private stable cached metadata", want: "v0.2.0", private: true},
		{name: "private pinned version", pin: "v0.1.0-alpha.1", want: "v0.1.0-alpha.1", private: true},
		{name: "pagination", channel: "beta", releases: append(make([]release, 100), release{Tag: "v1.0.0-alpha.1", Prerelease: true}), want: "v1.0.0-alpha.1"},
		{name: "empty channel", channel: "beta", failure: "no published release"},
		{name: "no stable release", noStable: true, failure: "SKALI_CHANNEL=beta"},
		{name: "invalid metadata", raw: `{"tag_name":"unfinished`, failure: "invalid release metadata"},
		{name: "stable rejects prerelease metadata", raw: `{"tag_name":"v0.3.0-rc.1","draft":false,"prerelease":true}`, failure: "no published release"},
		{name: "invalid channel", channel: "nightly", failure: "SKALI_CHANNEL must be"},
		{name: "invalid version", pin: "../../main", failure: "SKALI_VERSION must be"},
		{name: "checksum mismatch", want: "v0.2.0", badChecksum: true, failure: "checksum mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			toolsDir := filepath.Join(root, "tools")
			dest := filepath.Join(root, "installed")
			require.NoError(t, os.MkdirAll(toolsDir, 0755))
			require.NoError(t, os.MkdirAll(dest, 0755))
			shims := map[string]string{
				"curl":    `exec "$SKALI_TEST_EXECUTABLE" -test.run=^TestInstallCurlHelper$ -- "$@"`,
				"uname":   `case "$1" in -s) echo Linux ;; -m) echo x86_64 ;; esac`,
				"id":      `echo 0`,
				"install": `cp "$3" "$SKALI_TEST_DEST/${4##*/}"`,
				"mkdir":   `exit 0`,
			}
			for name, script := range shims {
				require.NoError(t, os.WriteFile(filepath.Join(toolsDir, name), []byte("#!/bin/sh\n"+script+"\n"), 0755))
			}
			assets := map[string]string{"skali_linux_amd64": "fixture CLI"}
			checksums := ""
			for name, body := range assets {
				checksums += fmt.Sprintf("%x  %s\n", sha256.Sum256([]byte(body)), name)
			}
			assets["checksums.txt"] = checksums
			if tc.badChecksum {
				assets["skali_linux_amd64"] = "corrupted CLI"
			}
			var mu sync.Mutex
			var requests []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				original := r.URL.Query().Get("url")
				mu.Lock()
				requests = append(requests, original)
				mu.Unlock()
				if strings.HasPrefix(original, "https://api.github.com/") && tc.private {
					require.Equal(t, "Bearer synthetic-fixture-token", r.Header.Get("Authorization"))
				}
				encode := func(v any) {
					enc := json.NewEncoder(w)
					if tc.pretty {
						enc.SetIndent("", "  ")
					}
					require.NoError(t, enc.Encode(v))
				}
				const api = "https://api.github.com/repos/Hinkolas/skali/releases"
				switch {
				case original == api+"/latest":
					if tc.raw != "" {
						fmt.Fprint(w, tc.raw)
						return
					}
					if tc.noStable {
						http.NotFound(w, r)
						return
					}
					encode(release{Tag: "v0.2.0", Assets: assetMetadata})
				case strings.HasPrefix(original, api+"?"):
					if strings.HasSuffix(original, "page=1") {
						if len(tc.releases) > 100 {
							encode(tc.releases[:100])
						} else if tc.releases == nil {
							encode([]release{})
						} else {
							encode(tc.releases)
						}
					} else {
						encode(tc.releases[100:])
					}
				case original == api+"/tags/"+tc.want:
					encode(release{Tag: tc.want, Assets: assetMetadata})
				case strings.HasPrefix(original, api+"/assets/"):
					require.Equal(t, "application/octet-stream", r.Header.Get("Accept"))
					names := map[string]string{"1": "skali_linux_amd64", "2": "skali-hostd_linux_amd64", "3": "checksums.txt"}
					fmt.Fprint(w, assets[names[strings.TrimPrefix(original, api+"/assets/")]])
				case strings.HasPrefix(original, "https://github.com/Hinkolas/skali/releases/download/"+tc.want+"/"):
					fmt.Fprint(w, assets[filepath.Base(original)])
				default:
					t.Errorf("unexpected download: %s", original)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			executable, err := os.Executable()
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "sh", "../../install.sh")
			for _, entry := range os.Environ() {
				key, _, _ := strings.Cut(entry, "=")
				if key == "PATH" || key == "GITHUB_TOKEN" || strings.HasPrefix(key, "SKALI_") {
					continue
				}
				cmd.Env = append(cmd.Env, entry)
			}
			cmd.Env = append(cmd.Env, "PATH="+toolsDir+":"+os.Getenv("PATH"), "SKALI_TEST_EXECUTABLE="+executable, "SKALI_TEST_SERVER="+server.URL, "SKALI_TEST_DEST="+dest, "SKALI_CHANNEL="+tc.channel, "SKALI_VERSION="+tc.pin)
			if tc.private {
				cmd.Env = append(cmd.Env, "GITHUB_TOKEN=synthetic-fixture-token")
			}
			output, err := cmd.CombinedOutput()
			if tc.failure != "" {
				require.Error(t, err, string(output))
				require.Contains(t, string(output), tc.failure)
				_, err = os.Stat(filepath.Join(dest, "skali"))
				require.ErrorIs(t, err, os.ErrNotExist)
				return
			}
			require.NoError(t, err, string(output))
			require.Contains(t, string(output), "("+tc.want+")")
			got, err := os.ReadFile(filepath.Join(dest, "skali"))
			require.NoError(t, err)
			require.Equal(t, "fixture CLI", string(got))
			entries, err := os.ReadDir(dest)
			require.NoError(t, err)
			require.Len(t, entries, 1, "the CLI is the only binary installed; skali cluster fetches skali-hostd itself")
			if tc.pin != "" && tc.pin != "latest" {
				mu.Lock()
				defer mu.Unlock()
				for _, request := range requests {
					require.NotContains(t, request, "/latest")
					require.NotContains(t, request, "?per_page=")
				}
			}
		})
	}
}

func TestInstallCurlHelper(t *testing.T) {
	server := os.Getenv("SKALI_TEST_SERVER")
	if server == "" {
		return
	}
	var target, output string
	headers := http.Header{}
	args := os.Args
	for i, arg := range args {
		if arg == "--" {
			args = args[i+1:]
			break
		}
	}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-o":
			i++
			output = args[i]
		case "-H":
			i++
			k, v, _ := strings.Cut(args[i], ":")
			headers.Add(k, strings.TrimSpace(v))
		default:
			if !strings.HasPrefix(args[i], "-") {
				target = args[i]
			}
		}
	}
	request, err := http.NewRequest("GET", server+"/?url="+url.QueryEscape(target), nil)
	if err != nil {
		os.Exit(2)
	}
	request.Header = headers
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(7)
	}
	defer response.Body.Close()
	if response.StatusCode >= 400 {
		os.Exit(22)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil || os.WriteFile(output, body, 0600) != nil {
		os.Exit(23)
	}
	os.Exit(0)
}
