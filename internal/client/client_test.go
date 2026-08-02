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

// The target endpoint wraps its payload; the client must unwrap it
// (regression: the original method decoded the bare object and returned
// zero values for every field).
func TestTargetDecodesWrapper(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "GET", r.Method)
		require.Equal(t, "/v1/environments/env-1/target", r.URL.Path)
		_, _ = w.Write([]byte(`{"target":{"target_revision_id":"rev-2","active_revision_id":"rev-1","updated_at":"2026-08-01T00:00:00Z"}}`))
	}))
	defer srv.Close()

	target, err := New(srv.URL, "tok", "").Target(context.Background(), "env-1")
	require.NoError(t, err)
	require.NotNil(t, target.TargetRevisionID)
	require.Equal(t, "rev-2", *target.TargetRevisionID)
	require.NotNil(t, target.ActiveRevisionID)
	require.Equal(t, "rev-1", *target.ActiveRevisionID)
	require.False(t, target.UpdatedAt.IsZero())
}

func TestListRevisionsDecodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "GET", r.Method)
		require.Equal(t, "/v1/environments/env-1/revisions", r.URL.Path)
		_, _ = w.Write([]byte(`{"revisions":[{"id":"rev-2","definition_version_id":"dv-1","checksum":"cafe","created_at":"2026-08-01T00:00:00Z"}]}`))
	}))
	defer srv.Close()

	revisions, err := New(srv.URL, "tok", "").ListRevisions(context.Background(), "env-1")
	require.NoError(t, err)
	require.Len(t, revisions, 1)
	require.Equal(t, "rev-2", revisions[0].ID)
	require.Equal(t, "dv-1", revisions[0].DefinitionVersionID)
}

func TestSetTargetSendsRevisionAndDecodesRun(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "PUT", r.Method)
		require.Equal(t, "/v1/environments/env-1/target", r.URL.Path)
		var body map[string]string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, "rev-1", body["revision_id"])
		_, _ = w.Write([]byte(`{"target":{"target_revision_id":"rev-1","active_revision_id":"rev-2","updated_at":"2026-08-01T00:00:00Z"},"run_id":"run-9"}`))
	}))
	defer srv.Close()

	result, err := New(srv.URL, "tok", "").SetTarget(context.Background(), "env-1", "rev-1")
	require.NoError(t, err)
	require.Equal(t, "run-9", result.RunID)
	require.Equal(t, "rev-1", *result.Target.TargetRevisionID)
}
