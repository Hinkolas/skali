package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoginDecodesSessionAndSendsHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "POST", r.Method)
		require.Equal(t, "/v1/auth/login", r.URL.Path)
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))
		require.Equal(t, "skali/test (host)", r.Header.Get("User-Agent"))
		require.Empty(t, r.Header.Get("Authorization"), "login is a public endpoint")

		var body map[string]string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, "nick@example.com", body["email"])

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"session":{"token":"tok123","expires_at":"2026-08-01T00:00:00Z","user":{"id":"u1","email":"nick@example.com","name":"","two_factor_enabled":false,"created_at":"2026-07-01T00:00:00Z"}}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/", "", "skali/test (host)") // trailing slash is normalized
	res, err := c.Login(context.Background(), "nick@example.com", "pw")
	require.NoError(t, err)
	require.Nil(t, res.Challenge)
	require.Equal(t, "tok123", res.Session.Token)
	require.Equal(t, "nick@example.com", res.Session.User.Email)
}

func TestLoginDecodesChallenge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"challenge":{"token":"ch123","expires_at":"2026-08-01T00:00:00Z"}}`))
	}))
	defer srv.Close()

	res, err := New(srv.URL, "", "").Login(context.Background(), "a@b.c", "pw")
	require.NoError(t, err)
	require.Nil(t, res.Session)
	require.Equal(t, "ch123", res.Challenge.Token)
}

func TestErrorEnvelopeBecomesAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"invalid_credentials","message":"invalid email or password"}}`))
	}))
	defer srv.Close()

	_, err := New(srv.URL, "", "").Login(context.Background(), "a@b.c", "wrong")
	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, http.StatusUnauthorized, apiErr.Status)
	require.Equal(t, "invalid_credentials", apiErr.Code)
	require.Contains(t, apiErr.Error(), "invalid_credentials")
}

func TestBearerTokenAttached(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer tok123", r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	require.NoError(t, New(srv.URL, "tok123", "").Logout(context.Background()))
}

func TestNonEnvelopeErrorBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream exploded"))
	}))
	defer srv.Close()

	err := New(srv.URL, "", "").Health(context.Background())
	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, http.StatusBadGateway, apiErr.Status)
	require.Equal(t, "internal", apiErr.Code)
	require.Equal(t, "upstream exploded", apiErr.Message)
}
