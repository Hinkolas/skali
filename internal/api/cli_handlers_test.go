package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/cliassets"
)

// shippedCLI stages a fake shipped-binaries directory and loads it.
func shippedCLI(t *testing.T, binaries map[string][]byte) *cliassets.Store {
	t.Helper()
	dir := t.TempDir()
	for name, body := range binaries {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), body, 0o755))
	}
	store, err := cliassets.Load(dir)
	require.NoError(t, err)
	return store
}

func TestCLIDownloadRoutes(t *testing.T) {
	binary := []byte("#!/bin/sh\necho skali version v0.4.0\n")
	sum := sha256.Sum256(binary)
	a := newTestAPIWith(t, "v0.4.0", func(d *Deps) {
		d.CLI = shippedCLI(t, map[string][]byte{"skali_darwin_arm64": binary, "skali_linux_amd64": []byte("linux")})
	})
	token := a.adminToken()

	get := func(path, clientVersion, bearer string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, a.srv.URL+path, nil)
		require.NoError(t, err)
		if clientVersion != "" {
			req.Header.Set(ClientVersionHeader, clientVersion)
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		res, err := a.srv.Client().Do(req)
		require.NoError(t, err)
		return res
	}

	// The listing names every platform with its digest, and answers a CLI
	// of the wrong release: the gate does not apply here.
	res := get("/v1/system/cli", "v0.3.2", token)
	defer res.Body.Close()
	require.Equal(t, http.StatusOK, res.StatusCode)
	var listing cliListingPayload
	require.NoError(t, json.NewDecoder(res.Body).Decode(&listing))
	require.Equal(t, "v0.4.0", listing.Version)
	require.True(t, listing.Enabled)
	require.Empty(t, listing.Reason)
	require.Len(t, listing.Platforms, 2)
	require.Equal(t, "darwin_arm64", listing.Platforms[0].Platform)
	require.Equal(t, hex.EncodeToString(sum[:]), listing.Platforms[0].SHA256)
	require.Equal(t, int64(len(binary)), listing.Platforms[0].Size)

	// The download streams the bytes with their size and digest.
	res = get("/v1/system/cli/darwin_arm64", "v0.3.2", token)
	defer res.Body.Close()
	require.Equal(t, http.StatusOK, res.StatusCode)
	require.Equal(t, "application/octet-stream", res.Header.Get("Content-Type"))
	require.Equal(t, hex.EncodeToString(sum[:]), res.Header.Get("Skali-CLI-SHA256"))
	require.Equal(t, "v0.4.0", res.Header.Get(VersionHeader))
	body, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	require.Equal(t, binary, body)

	// The /api alias serves it too.
	res = get("/api/v1/system/cli/linux_amd64", "", token)
	defer res.Body.Close()
	require.Equal(t, http.StatusOK, res.StatusCode)

	// Unknown and malformed platforms are 404 not_found.
	for _, platform := range []string{"windows_amd64", "linux", "..%2Fetc"} {
		res = get("/v1/system/cli/"+platform, "", token)
		res.Body.Close()
		require.Equal(t, http.StatusNotFound, res.StatusCode, platform)
	}

	// Members only.
	res = get("/v1/system/cli", "", "")
	res.Body.Close()
	require.Equal(t, http.StatusUnauthorized, res.StatusCode)
	res = get("/v1/system/cli/darwin_arm64", "", "")
	res.Body.Close()
	require.Equal(t, http.StatusUnauthorized, res.StatusCode)
}

func TestCLIDownloadNotServed(t *testing.T) {
	cases := []struct {
		name   string
		adjust func(*Deps)
		reason string
	}{
		{name: "not shipped", adjust: func(d *Deps) { d.CLI = nil }, reason: "not_shipped"},
		{name: "disabled", adjust: func(d *Deps) {
			d.CLI = shippedCLI(t, map[string][]byte{"skali_linux_amd64": []byte("linux")})
			d.ServeCLI = false
		}, reason: "disabled"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := newTestAPIWith(t, "v0.4.0", tc.adjust)
			token := a.adminToken()

			status, body := a.do("GET", "/v1/system/cli", token, nil)
			require.Equal(t, http.StatusOK, status)
			require.Equal(t, false, body["enabled"])
			require.Equal(t, tc.reason, body["reason"])
			require.Empty(t, body["platforms"])

			status, body = a.do("GET", "/v1/system/cli/linux_amd64", token, nil)
			require.Equal(t, http.StatusNotFound, status)
			detail := body["error"].(map[string]any)
			require.Equal(t, codeCLINotServed, detail["code"])
			require.Contains(t, detail["message"], "v0.4.0")
		})
	}
}
