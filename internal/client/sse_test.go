package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// A stream request carries the same identity headers as a JSON request, and
// a refusal before the stream opens decodes as an ordinary API error with
// the daemon version observed.
func TestStreamStampsHeadersAndDecodesRefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "text/event-stream", r.Header.Get("Accept"))
		require.Equal(t, "Bearer tok", r.Header.Get("Authorization"))
		require.Equal(t, "skali/test (host)", r.Header.Get("User-Agent"))
		require.Equal(t, "v1.2.3", r.Header.Get(ClientVersionHeader))
		require.Equal(t, "e-7", r.Header.Get("Last-Event-ID"))
		w.Header().Set(VersionHeader, "v9.9.9")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":{"code":"cli_version_mismatch","message":"this cluster runs skalid v9.9.9 and requires skali v9.9.9 (this CLI is v1.2.3)"}}`))
	}))
	t.Cleanup(srv.Close)

	c := New(srv.URL, "tok", Caller{UserAgent: "skali/test (host)", Version: "v1.2.3"})
	_, err := c.Stream(context.Background(), "/v1/runs/r1/stream", "e-7")
	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, CodeCLIVersionMismatch, apiErr.Code)
	require.Equal(t, http.StatusConflict, apiErr.Status)
	require.Equal(t, "v9.9.9", c.ObservedVersion())
}
