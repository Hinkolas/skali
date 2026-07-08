package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
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

// SetErr flips the error seam under the lock — imports now run in a
// background goroutine, so the direct field write would race.
func (s *stubRegistryOps) SetErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Err = err
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
		{"GET", "/v1/operations"},
		{"GET", "/v1/operations/" + uuid.NewString()},
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

	// Imports are async: 202 with a pollable operation, catalog row in its
	// result.
	status, body := a.do("POST", "/v1/registry/images", token, map[string]any{"reference": "postgres"})
	require.Equal(t, http.StatusAccepted, status, "body: %v", body)
	op := body["operation"].(map[string]any)
	require.Equal(t, "registry_import", op["kind"])
	require.Equal(t, "postgres", op["subject"])

	op = a.waitOperation(token, op["id"].(string))
	require.Equal(t, "succeeded", op["status"])
	img := op["result"].(map[string]any)["image"].(map[string]any)
	require.Equal(t, "mirror/docker.io/library/postgres", img["repository"])
	require.Equal(t, "sha256:stub-postgres", img["digest"])

	status, body = a.do("GET", "/v1/registry/images", token, nil)
	require.Equal(t, http.StatusOK, status)
	require.Len(t, body["images"].([]any), 1)
	id := body["images"].([]any)[0].(map[string]any)["id"].(string)

	status, _ = a.do("DELETE", "/v1/registry/images/"+id, token, nil)
	require.Equal(t, http.StatusNoContent, status)
	status, body = a.do("GET", "/v1/registry/images", token, nil)
	require.Equal(t, http.StatusOK, status)
	require.Empty(t, body["images"])

	// Bad input fails synchronously, before anything goes async.
	status, body = a.do("POST", "/v1/registry/images", token, map[string]any{})
	require.Equal(t, http.StatusBadRequest, status)
	require.Equal(t, "bad_request", errorCode(t, body))
	status, body = a.do("POST", "/v1/registry/images", token,
		map[string]any{"reference": "postgres@sha256:" + strings.Repeat("a", 64)})
	require.Equal(t, http.StatusBadRequest, status)
	require.Equal(t, "bad_request", errorCode(t, body), "digest refs rejected synchronously")

	status, body = a.do("DELETE", "/v1/registry/images/"+uuid.NewString(), token, nil)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, "not_found", errorCode(t, body), "unknown catalog id")
	status, body = a.do("DELETE", "/v1/registry/images/not-a-uuid", token, nil)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, "not_found", errorCode(t, body), "malformed id")

	// Upstream and mirror failures happen after the 202: they land on the
	// operation, not the HTTP response.
	a.reg.SetErr(mirror.ErrUpstreamNotFound)
	status, body = a.do("POST", "/v1/registry/images", token, map[string]any{"reference": "missing:1"})
	require.Equal(t, http.StatusAccepted, status)
	op = a.waitOperation(token, body["operation"].(map[string]any)["id"].(string))
	require.Equal(t, "failed", op["status"])
	require.Contains(t, op["error"], "not found")
}

func TestOperationsListAndFilters(t *testing.T) {
	a := newTestAPI(t)
	a.createAdmin("admin@example.com", "hunter2hunter2")
	token := a.login("admin@example.com", "hunter2hunter2")

	// One import that succeeds, one that fails.
	status, body := a.do("POST", "/v1/registry/images", token, map[string]any{"reference": "alpine:3"})
	require.Equal(t, http.StatusAccepted, status)
	a.waitOperation(token, body["operation"].(map[string]any)["id"].(string))

	a.reg.SetErr(mirror.ErrRegistryUnavailable)
	status, body = a.do("POST", "/v1/registry/images", token, map[string]any{"reference": "bad:1"})
	require.Equal(t, http.StatusAccepted, status)
	a.waitOperation(token, body["operation"].(map[string]any)["id"].(string))

	status, body = a.do("GET", "/v1/operations", token, nil)
	require.Equal(t, http.StatusOK, status)
	require.Len(t, body["operations"].([]any), 2)
	// Newest first.
	first := body["operations"].([]any)[0].(map[string]any)
	require.Equal(t, "bad:1", first["subject"])

	status, body = a.do("GET", "/v1/operations?status=failed", token, nil)
	require.Equal(t, http.StatusOK, status)
	require.Len(t, body["operations"].([]any), 1)

	status, body = a.do("GET", "/v1/operations?kind=registry_import&status=succeeded", token, nil)
	require.Equal(t, http.StatusOK, status)
	require.Len(t, body["operations"].([]any), 1)

	status, body = a.do("GET", "/v1/operations?status=bogus", token, nil)
	require.Equal(t, http.StatusBadRequest, status)
	require.Equal(t, "bad_request", errorCode(t, body))

	status, body = a.do("GET", "/v1/operations/"+uuid.NewString(), token, nil)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, "not_found", errorCode(t, body))
	status, body = a.do("GET", "/v1/operations/not-a-uuid", token, nil)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, "not_found", errorCode(t, body))
}

func TestRegistryDisabledWithoutClusterAddr(t *testing.T) {
	a := newTestAPIWithRegistry(t, nil)
	a.createAdmin("admin@example.com", "hunter2hunter2")
	token := a.login("admin@example.com", "hunter2hunter2")

	status, body := a.do("GET", "/v1/registry/images", token, nil)
	require.Equal(t, http.StatusServiceUnavailable, status)
	require.Equal(t, "registry_disabled", errorCode(t, body))
}
