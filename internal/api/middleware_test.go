package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func serveWithClientVersion(t *testing.T, server, client string) *httptest.ResponseRecorder {
	t.Helper()
	handler := requireClientVersion(server)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/v1/anything", nil)
	if client != "" {
		req.Header.Set(ClientVersionHeader, client)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// A released daemon refuses a released CLI of another version and names the
// version it requires; everything that is not two different releases passes.
func TestRequireClientVersion(t *testing.T) {
	for _, client := range []string{"", "v0.4.0", "v0.0.0-dev", "v0.4.0-3-gabc1234", "test"} {
		rec := serveWithClientVersion(t, "v0.4.0", client)
		require.Equal(t, http.StatusOK, rec.Code, "client %q", client)
	}

	for _, client := range []string{"v0.3.2", "v0.4.0-rc.1", "v0.5.0"} {
		rec := serveWithClientVersion(t, "v0.4.0", client)
		require.Equal(t, http.StatusConflict, rec.Code, "client %q", client)
		var body errorBody
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), "client %q", client)
		require.Equal(t, codeCLIVersionMismatch, body.Error.Code)
		require.Contains(t, body.Error.Message, "requires skali v0.4.0")
		require.Contains(t, body.Error.Message, "this CLI is "+client)
	}

	rec := serveWithClientVersion(t, "v0.4.0", "v0.3.2")
	var body errorBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, "this cluster runs skalid v0.4.0 and requires skali v0.4.0 (this CLI is v0.3.2)", body.Error.Message)
}

// A working-tree daemon (v0.0.0-dev, git describe, the test harness) never
// gates anyone: its version is not comparable.
func TestRequireClientVersionDevServerNeverGates(t *testing.T) {
	for _, server := range []string{"v0.0.0-dev", "v0.1.0-3-gabc1234", "test", ""} {
		rec := serveWithClientVersion(t, server, "v9.9.9")
		require.Equal(t, http.StatusOK, rec.Code, "server %q", server)
	}
}
