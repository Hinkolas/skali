package api

import (
	"bufio"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/journal"
)

// seedRun writes one run with a running step and attempt through the
// journal service (the API surface is read-only over the journal).
func (a *testAPI) seedRun(envID string) (runID, stepID, attemptID uuid.UUID) {
	a.t.Helper()
	ctx := context.Background()
	environmentID, err := uuid.Parse(envID)
	require.NoError(a.t, err)
	run, err := a.journal.CreateRun(ctx, journal.RunInput{
		Kind: "deployment", EnvironmentID: environmentID, Actor: "tester",
	})
	require.NoError(a.t, err)
	require.NoError(a.t, a.journal.StartRun(ctx, run.ID))
	step, err := a.journal.EnsureStep(ctx, run.ID, nil, "apply:web", "Apply web")
	require.NoError(a.t, err)
	require.NoError(a.t, a.journal.SetStepStatus(ctx, step.ID, journal.StepRunning))
	attempt, err := a.journal.StartAttempt(ctx, step.ID)
	require.NoError(a.t, err)
	return run.ID, step.ID, attempt.ID
}

func TestRunReadAPI(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("nick@example.com", "hunter2hunter2")
	token := a.login("nick@example.com", "hunter2hunter2")

	status, body := a.do("POST", "/v1/projects", token, map[string]any{"name": "demo"})
	require.Equal(t, http.StatusCreated, status)
	projectID := body["project"].(map[string]any)["id"].(string)
	status, body = a.do("POST", "/v1/projects/"+projectID+"/environments", token, map[string]any{"name": "production"})
	require.Equal(t, http.StatusCreated, status)
	envID := body["environment"].(map[string]any)["id"].(string)

	runID, stepID, attemptID := a.seedRun(envID)
	ctx := context.Background()
	require.NoError(t, a.journal.Append(ctx, attemptID, nil, "info", "first entry", nil))
	require.NoError(t, a.journal.Append(ctx, attemptID, nil, "error", "second entry", map[string]any{"code": "boom"}))

	status, body = a.do("GET", "/v1/environments/"+envID+"/runs", token, nil)
	require.Equal(t, http.StatusOK, status)
	runs := body["runs"].([]any)
	require.Len(t, runs, 1)
	require.Equal(t, runID.String(), runs[0].(map[string]any)["id"])
	require.Equal(t, "running", runs[0].(map[string]any)["status"])

	status, body = a.do("GET", "/v1/runs/"+runID.String(), token, nil)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "deployment", body["run"].(map[string]any)["kind"])
	steps := body["steps"].([]any)
	require.Len(t, steps, 1)
	step := steps[0].(map[string]any)
	require.Equal(t, "apply:web", step["key"])
	require.Len(t, step["attempts"], 1)

	status, body = a.do("GET", "/v1/steps/"+stepID.String()+"/logs", token, nil)
	require.Equal(t, http.StatusOK, status)
	logs := body["logs"].([]any)
	require.Len(t, logs, 2)
	require.Equal(t, "first entry", logs[0].(map[string]any)["message"])
	require.Equal(t, "1:2", body["next"])

	// Cursor pagination.
	status, body = a.do("GET", "/v1/steps/"+stepID.String()+"/logs?after=1:1", token, nil)
	require.Equal(t, http.StatusOK, status)
	logs = body["logs"].([]any)
	require.Len(t, logs, 1)
	require.Equal(t, "second entry", logs[0].(map[string]any)["message"])

	status, _ = a.do("GET", "/v1/runs/"+uuid.NewString(), token, nil)
	require.Equal(t, http.StatusNotFound, status)
}

// TestStepLogStream exercises the SSE endpoint: catch-up entries, then a
// live entry committed while the stream is open.
func TestStepLogStream(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("nick@example.com", "hunter2hunter2")
	token := a.login("nick@example.com", "hunter2hunter2")

	status, body := a.do("POST", "/v1/projects", token, map[string]any{"name": "demo"})
	require.Equal(t, http.StatusCreated, status)
	projectID := body["project"].(map[string]any)["id"].(string)
	status, body = a.do("POST", "/v1/projects/"+projectID+"/environments", token, map[string]any{"name": "production"})
	require.Equal(t, http.StatusCreated, status)
	envID := body["environment"].(map[string]any)["id"].(string)

	_, stepID, attemptID := a.seedRun(envID)
	ctx := context.Background()
	require.NoError(t, a.journal.Append(ctx, attemptID, nil, "info", "catch-up entry", nil))

	req, err := http.NewRequestWithContext(ctx, "GET", a.srv.URL+"/v1/steps/"+stepID.String()+"/logs/stream", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := a.srv.Client().Do(req)
	require.NoError(t, err)
	defer res.Body.Close()
	require.Equal(t, http.StatusOK, res.StatusCode)
	require.Equal(t, "text/event-stream", res.Header.Get("Content-Type"))

	reader := bufio.NewReader(res.Body)
	readEvent := func() (id, data string) {
		a.t.Helper()
		deadline := time.After(5 * time.Second)
		done := make(chan struct{})
		go func() {
			defer close(done)
			for {
				line, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				line = strings.TrimRight(line, "\n")
				switch {
				case strings.HasPrefix(line, "id: "):
					id = strings.TrimPrefix(line, "id: ")
				case strings.HasPrefix(line, "data: "):
					data = strings.TrimPrefix(line, "data: ")
				case line == "" && data != "":
					return
				}
			}
		}()
		select {
		case <-done:
		case <-deadline:
			a.t.Fatal("timed out waiting for SSE event")
		}
		return id, data
	}

	id, data := readEvent()
	require.Equal(t, "1:1", id)
	require.Contains(t, data, "catch-up entry")

	// A live entry arrives while the stream is open.
	require.NoError(t, a.journal.Append(ctx, attemptID, nil, "info", "live entry", nil))
	id, data = readEvent()
	require.Equal(t, "1:2", id)
	require.Contains(t, data, "live entry")
}
