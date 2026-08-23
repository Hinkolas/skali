package kube

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/client-go/rest"
)

func TestAPIServerReady(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/readyz", r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := NewFromConfig(&rest.Config{Host: server.URL})
	require.NoError(t, err)
	require.NoError(t, client.APIServerReady(context.Background()))
}

func TestAPIServerReadyNotReady(t *testing.T) {
	t.Parallel()
	// The k3s supervisor answers for a dead apiserver with this exact body.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("apiserver not ready"))
	}))
	defer server.Close()

	client, err := NewFromConfig(&rest.Config{Host: server.URL})
	require.NoError(t, err)
	err = client.APIServerReady(context.Background())
	require.ErrorContains(t, err, "apiserver not ready")
}

func TestAPIServerReadyUnreachable(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	host := server.URL
	server.Close()

	client, err := NewFromConfig(&rest.Config{Host: host})
	require.NoError(t, err)
	require.Error(t, client.APIServerReady(context.Background()))
}
