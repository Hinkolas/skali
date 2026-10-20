package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

const testDevCA = "-----BEGIN CERTIFICATE-----\nZGV2LWNh\n-----END CERTIFICATE-----\n"

func TestDevCAHandler(t *testing.T) {
	t.Parallel()
	recorder := httptest.NewRecorder()
	devCAHandler(testDevCA)(recorder, httptest.NewRequest(http.MethodGet, DevCAPath, nil))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "application/x-pem-file", recorder.Header().Get("Content-Type"))
	require.Equal(t, "no-store", recorder.Header().Get("Cache-Control"))
	require.Equal(t, testDevCA, recorder.Body.String())
}

// A local platform serves its development CA publicly under both path
// spellings, to any CLI release (the proxy's CLI may differ from the
// platform's); an installation without one has no such route.
func TestDevCARoute(t *testing.T) {
	get := func(t *testing.T, a *testAPI, path string) (int, string) {
		t.Helper()
		request, err := http.NewRequest(http.MethodGet, a.srv.URL+path, nil)
		require.NoError(t, err)
		request.Header.Set(ClientVersionHeader, "v0.3.2")
		response, err := a.srv.Client().Do(request)
		require.NoError(t, err)
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		return response.StatusCode, string(body)
	}

	local := newTestAPIWith(t, "v0.4.0", func(d *Deps) { d.DevCACert = testDevCA })
	for _, path := range []string{DevCAPath, "/api" + DevCAPath} {
		status, body := get(t, local, path)
		require.Equal(t, http.StatusOK, status, path)
		require.Equal(t, testDevCA, body, path)
	}

	production := newTestAPIWith(t, "v0.4.0", nil)
	status, _ := get(t, production, "/api"+DevCAPath)
	require.Equal(t, http.StatusNotFound, status)
}
