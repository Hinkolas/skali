package api

import (
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/reconcile"
)

// healthStub stands in for the kernel's health cache: no pass runs in the
// API harness, so tests seed verdicts directly and count the reads.
type healthStub struct {
	mu      sync.Mutex
	entries map[uuid.UUID]reconcile.EnvironmentHealth
	calls   int
	lastIDs []uuid.UUID
}

func (s *healthStub) EnvironmentHealths(ids []uuid.UUID) map[uuid.UUID]reconcile.EnvironmentHealth {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.lastIDs = append([]uuid.UUID(nil), ids...)
	result := make(map[uuid.UUID]reconcile.EnvironmentHealth, len(ids))
	for _, id := range ids {
		if entry, ok := s.entries[id]; ok {
			result[id] = entry
		}
	}
	return result
}

func newHealthAPI(t *testing.T) (*testAPI, *healthStub) {
	t.Helper()
	stub := &healthStub{entries: map[uuid.UUID]reconcile.EnvironmentHealth{}}
	a := newTestAPIWith(t, "test", func(d *Deps) { d.Health = stub })
	return a, stub
}

func summaryEnvironments(t *testing.T, body map[string]any) map[string]map[string]any {
	t.Helper()
	projects := body["projects"].([]any)
	require.Len(t, projects, 1)
	byName := map[string]map[string]any{}
	for _, raw := range projects[0].(map[string]any)["summary"].(map[string]any)["environments"].([]any) {
		env := raw.(map[string]any)
		byName[env["name"].(string)] = env
	}
	return byName
}

func TestProjectListSummaryReadsKernelHealth(t *testing.T) {
	a, stub := newHealthAPI(t)
	a.createUser("nick@example.com", "hunter2hunter2")
	token := a.login("nick@example.com", "hunter2hunter2")
	projectID, prodID := a.createEnvironment(t, token)
	status, body := a.do("POST", "/v1/projects/"+projectID+"/environments", token, map[string]any{"name": "staging"})
	require.Equal(t, http.StatusCreated, status, "%v", body)

	evaluatedAt := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)
	stub.entries[uuid.MustParse(prodID)] = reconcile.EnvironmentHealth{
		Health: module.HealthDegraded, EvaluatedAt: evaluatedAt,
	}

	status, body = a.do("GET", "/v1/projects?include=summary", token, nil)
	require.Equal(t, http.StatusOK, status)
	envs := summaryEnvironments(t, body)

	// The verdict and its time come straight from the cache.
	require.Equal(t, "degraded", envs["production"]["health"])
	require.Equal(t, evaluatedAt.Format(time.RFC3339), envs["production"]["health_evaluated_at"])
	// A miss reads unknown and carries no evaluation time.
	require.Equal(t, "unknown", envs["staging"]["health"])
	require.Nil(t, envs["staging"]["health_evaluated_at"])

	// One batch read per request covers every readable environment.
	require.Equal(t, 1, stub.calls)
	require.ElementsMatch(t, []uuid.UUID{uuid.MustParse(prodID), uuid.MustParse(envs["staging"]["id"].(string))}, stub.lastIDs)
}

func TestProjectListSummaryLockedEnvironmentHidesCachedHealth(t *testing.T) {
	a, stub := newHealthAPI(t)
	a.createUser("owner@example.com", "hunter2hunter2")
	a.createMember("bob@example.com", "hunter2hunter2")
	owner := a.login("owner@example.com", "hunter2hunter2")
	bob := a.login("bob@example.com", "hunter2hunter2")
	projectID, prodID := a.createEnvironment(t, owner)
	status, body := a.do("POST", "/v1/projects/"+projectID+"/environments", owner, map[string]any{"name": "staging"})
	require.Equal(t, http.StatusCreated, status, "%v", body)
	stagingID := body["environment"].(map[string]any)["id"].(string)

	now := time.Now().UTC().Truncate(time.Second)
	for _, id := range []string{prodID, stagingID} {
		stub.entries[uuid.MustParse(id)] = reconcile.EnvironmentHealth{Health: module.HealthHealthy, EvaluatedAt: now}
	}

	// Bob reads the project but production is locked for him.
	a.grantMember(t, projectID, "bob@example.com", "read")
	a.setCell(t, prodID, "bob@example.com", "none")

	status, body = a.do("GET", "/v1/projects?include=summary", bob, nil)
	require.Equal(t, http.StatusOK, status)
	envs := summaryEnvironments(t, body)
	require.Equal(t, "none", envs["production"]["access"])
	require.Nil(t, envs["production"]["state"])
	require.Nil(t, envs["production"]["health"], "a cached verdict never leaks through a lock")
	require.Nil(t, envs["production"]["health_evaluated_at"])
	require.Equal(t, "healthy", envs["staging"]["health"])
	require.NotNil(t, envs["staging"]["health_evaluated_at"])

	// The locked environment is never even asked for.
	require.Equal(t, []uuid.UUID{uuid.MustParse(stagingID)}, stub.lastIDs)
}
