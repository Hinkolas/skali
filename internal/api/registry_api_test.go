package api

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/mirror"
	"github.com/Hinkolas/skali/internal/store"
)

// stubRegistryOps is an in-memory RegistryOps: enough catalog semantics for
// the handler tests, plus an error seam for sentinel mapping.
type stubRegistryOps struct {
	mu   sync.Mutex
	rows []store.RegistryImage
	// Err, when set, fails every call with it (sentinel-mapping seam).
	Err error
}

func (s *stubRegistryOps) List(context.Context) ([]store.RegistryImage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Err != nil {
		return nil, s.Err
	}
	return append([]store.RegistryImage(nil), s.rows...), nil
}

func (s *stubRegistryOps) Import(_ context.Context, reference string) (store.RegistryImage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Err != nil {
		return store.RegistryImage{}, s.Err
	}
	row := store.RegistryImage{
		ID:         uuid.New(),
		Repository: "mirror/docker.io/library/" + reference,
		Tag:        "latest",
		Digest:     "sha256:stub-" + reference,
	}
	s.rows = append(s.rows, row)
	return row, nil
}

func (s *stubRegistryOps) Delete(_ context.Context, id uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Err != nil {
		return s.Err
	}
	for i, r := range s.rows {
		if r.ID == id {
			s.rows = append(s.rows[:i], s.rows[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("%w: %s", mirror.ErrImageNotFound, id)
}

func TestRegistryRequiresAdmin(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("member@example.com", "hunter2hunter2")
	token := a.login("member@example.com", "hunter2hunter2")

	for _, tc := range []struct{ method, path string }{
		{"GET", "/v1/registry/images"},
		{"POST", "/v1/registry/images"},
		{"DELETE", "/v1/registry/images/" + uuid.NewString()},
	} {
		status, body := a.do(tc.method, tc.path, token, map[string]any{})
		require.Equal(t, http.StatusForbidden, status, "%s %s", tc.method, tc.path)
		require.Equal(t, "forbidden", errorCode(t, body), "%s %s", tc.method, tc.path)
	}
}

func TestRegistryWritesGatedBySudoMode(t *testing.T) {
	a := newTestAPI(t)
	a.createAdmin("admin@example.com", "hunter2hunter2")
	token := a.login("admin@example.com", "hunter2hunter2")

	a.staleAllSessions()

	// Reads stay open for a stale admin.
	status, _ := a.do("GET", "/v1/registry/images", token, nil)
	require.Equal(t, http.StatusOK, status)

	status, body := a.do("POST", "/v1/registry/images", token, map[string]any{"reference": "postgres:17"})
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, "reauth_required", errorCode(t, body))
}

func TestRegistryImageLifecycleViaAPI(t *testing.T) {
	a := newTestAPI(t)
	a.createAdmin("admin@example.com", "hunter2hunter2")
	token := a.login("admin@example.com", "hunter2hunter2")

	status, body := a.do("POST", "/v1/registry/images", token, map[string]any{"reference": "postgres"})
	require.Equal(t, http.StatusCreated, status, "body: %v", body)
	img := body["image"].(map[string]any)
	require.Equal(t, "mirror/docker.io/library/postgres", img["repository"])
	require.Equal(t, "sha256:stub-postgres", img["digest"])
	id := img["id"].(string)

	status, body = a.do("GET", "/v1/registry/images", token, nil)
	require.Equal(t, http.StatusOK, status)
	require.Len(t, body["images"].([]any), 1)

	status, _ = a.do("DELETE", "/v1/registry/images/"+id, token, nil)
	require.Equal(t, http.StatusNoContent, status)
	status, body = a.do("GET", "/v1/registry/images", token, nil)
	require.Equal(t, http.StatusOK, status)
	require.Empty(t, body["images"])

	// Validation and sentinel mapping.
	status, body = a.do("POST", "/v1/registry/images", token, map[string]any{})
	require.Equal(t, http.StatusBadRequest, status)
	require.Equal(t, "bad_request", errorCode(t, body))

	status, body = a.do("DELETE", "/v1/registry/images/"+uuid.NewString(), token, nil)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, "not_found", errorCode(t, body), "unknown catalog id")
	status, body = a.do("DELETE", "/v1/registry/images/not-a-uuid", token, nil)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, "not_found", errorCode(t, body), "malformed id")

	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{mirror.ErrInvalidReference, http.StatusBadRequest, "bad_request"},
		{mirror.ErrUpstreamNotFound, http.StatusNotFound, "not_found"},
		{mirror.ErrRegistryUnavailable, http.StatusServiceUnavailable, "registry_unavailable"},
	} {
		a.reg.Err = tc.err
		status, body = a.do("POST", "/v1/registry/images", token, map[string]any{"reference": "x:1"})
		require.Equal(t, tc.status, status, "%v", tc.err)
		require.Equal(t, tc.code, errorCode(t, body), "%v", tc.err)
	}
}

func TestRegistryDisabledWithoutClusterAddr(t *testing.T) {
	a := newTestAPIWithRegistry(t, nil)
	a.createAdmin("admin@example.com", "hunter2hunter2")
	token := a.login("admin@example.com", "hunter2hunter2")

	status, body := a.do("GET", "/v1/registry/images", token, nil)
	require.Equal(t, http.StatusServiceUnavailable, status)
	require.Equal(t, "registry_disabled", errorCode(t, body))
}
