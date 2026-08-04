package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func instanceServer(t *testing.T, id string, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id != "" {
			w.Header().Set(InstanceHeader, id)
			w.Header().Set(VersionHeader, "v9.9.9")
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestInstanceAdoptedOnFirstUse(t *testing.T) {
	srv := instanceServer(t, "install-a", http.StatusOK, `{"status":"ok"}`)

	c := New(srv.URL, "", "")
	var adopted []string
	c.PinInstance("", func(observed string) { adopted = append(adopted, observed) })

	require.NoError(t, c.Health(context.Background()))
	require.Equal(t, []string{"install-a"}, adopted)
	require.Equal(t, "install-a", c.ObservedInstance())
	require.Equal(t, "v9.9.9", c.ObservedVersion())

	// The adopted identity is the pin now: matching responses pass, and
	// the adoption callback does not fire again.
	require.NoError(t, c.Health(context.Background()))
	require.Equal(t, []string{"install-a"}, adopted)
}

func TestInstanceMismatchFailsRequest(t *testing.T) {
	srv := instanceServer(t, "install-b", http.StatusOK, `{"status":"ok"}`)

	c := New(srv.URL, "", "")
	c.PinInstance("install-a", func(string) { t.Fatal("mismatch must never adopt") })

	err := c.Health(context.Background())
	mismatch, ok := errors.AsType[*InstanceMismatchError](err)
	require.True(t, ok, "want InstanceMismatchError, got %v", err)
	require.Equal(t, "install-a", mismatch.Pinned)
	require.Equal(t, "install-b", mismatch.Observed)
}

func TestInstanceMismatchOutranksErrorEnvelope(t *testing.T) {
	// A reinstalled cluster answers 401 for the stale token; the identity
	// change is the explanation and must win over the APIError.
	srv := instanceServer(t, "install-b", http.StatusUnauthorized,
		`{"error":{"code":"invalid_token","message":"invalid or expired token"}}`)

	c := New(srv.URL, "stale-token", "")
	c.PinInstance("install-a", nil)

	_, err := c.CurrentSession(context.Background())
	_, ok := errors.AsType[*InstanceMismatchError](err)
	require.True(t, ok, "want InstanceMismatchError, got %v", err)
}

func TestInstanceHeaderAbsentVerifiesNothing(t *testing.T) {
	srv := instanceServer(t, "", http.StatusOK, `{"status":"ok"}`)

	c := New(srv.URL, "", "")
	c.PinInstance("install-a", func(string) { t.Fatal("nothing observed, nothing to adopt") })

	require.NoError(t, c.Health(context.Background()))
	require.Empty(t, c.ObservedInstance())
	require.Empty(t, c.ObservedVersion())
}

func TestStreamChecksInstance(t *testing.T) {
	srv := instanceServer(t, "install-b", http.StatusOK, "data: {}\n\n")

	c := New(srv.URL, "", "")
	c.PinInstance("install-a", nil)

	_, err := c.Stream(context.Background(), "/v1/runs/r1/stream", "")
	_, ok := errors.AsType[*InstanceMismatchError](err)
	require.True(t, ok, "want InstanceMismatchError, got %v", err)
}
