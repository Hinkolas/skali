package api

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Hinkolas/skali/internal/cliassets"
)

// cliHandlers hands authenticated members the skali release this daemon
// runs (docs/versioning.md, decision 2): the listing names every platform
// with its digest, the download streams one binary. The routes sit outside
// the CLI version gate on purpose, since a mismatched CLI calls them to
// fetch its fix, and outside the request timeout, since a binary on a slow
// link outlives it.
type cliHandlers struct {
	version string
	store   *cliassets.Store // nil: the image ships no binaries
	enabled bool             // false: the operator turned serving off
}

type cliListingPayload struct {
	Version   string            `json:"version"`
	Enabled   bool              `json:"enabled"`
	Reason    string            `json:"reason,omitempty"`
	Platforms []cliAssetPayload `json:"platforms"`
}

type cliAssetPayload struct {
	Platform string `json:"platform"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
}

// reason names why nothing is served, empty when serving works.
func (h *cliHandlers) reason() string {
	switch {
	case !h.enabled:
		return "disabled"
	case h.store == nil:
		return "not_shipped"
	}
	return ""
}

func (h *cliHandlers) list(w http.ResponseWriter, r *http.Request) {
	payload := cliListingPayload{Version: h.version, Platforms: []cliAssetPayload{}}
	if reason := h.reason(); reason != "" {
		payload.Reason = reason
		writeJSON(w, http.StatusOK, payload)
		return
	}
	payload.Enabled = true
	for _, asset := range h.store.Assets() {
		payload.Platforms = append(payload.Platforms, cliAssetPayload{
			Platform: asset.Platform, SHA256: asset.SHA256, Size: asset.Size,
		})
	}
	writeJSON(w, http.StatusOK, payload)
}

func (h *cliHandlers) download(w http.ResponseWriter, r *http.Request) {
	platform := chi.URLParam(r, "platform")
	if !cliassets.ValidPlatform(platform) {
		writeError(w, http.StatusNotFound, codeNotFound, "unknown platform; use <goos>_<goarch>")
		return
	}
	if h.reason() != "" {
		writeError(w, http.StatusNotFound, codeCLINotServed,
			fmt.Sprintf("this cluster does not serve the skali CLI; install skali %s from the release feed or your own distribution", h.version))
		return
	}
	file, asset, err := h.store.Open(platform)
	if err != nil {
		writeError(w, http.StatusNotFound, codeNotFound, "this cluster ships no skali for "+platform)
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(asset.Size, 10))
	w.Header().Set("Skali-CLI-SHA256", asset.SHA256)
	http.ServeContent(w, r, "skali_"+platform, time.Time{}, file)
}
