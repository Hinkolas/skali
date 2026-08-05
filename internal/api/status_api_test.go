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

	"github.com/Hinkolas/skali/internal/module"
)

func (a *testAPI) createEnvironment(t *testing.T, token string) (projectID, environmentID string) {
	t.Helper()
	status, body := a.do("POST", "/v1/projects", token, map[string]any{"name": "demo"})
	require.Equal(t, http.StatusCreated, status)
	projectID = body["project"].(map[string]any)["id"].(string)
	status, body = a.do("POST", "/v1/projects/"+projectID+"/environments", token, map[string]any{"name": "production"})
	require.Equal(t, http.StatusCreated, status)
	environmentID = body["environment"].(map[string]any)["id"].(string)
	return projectID, environmentID
}

func TestEnvironmentStatus(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("status@example.com", "hunter2hunter2")
	token := a.login("status@example.com", "hunter2hunter2")
	_, envID := a.createEnvironment(t, token)

	// Before any observation sync: unknown, no revisions, no services.
	status, body := a.do("GET", "/v1/environments/"+envID+"/status", token, nil)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, envID, body["environment_id"])
	require.Nil(t, body["target_revision"])
	require.Nil(t, body["active_revision"])
	observation := body["observation"].(map[string]any)
	require.Equal(t, "unknown", observation["state"])
	require.Empty(t, body["services"])
	require.NotNil(t, body["platforms"], "platforms must be an empty list, not null")
	require.Empty(t, body["platforms"])

	// A synced fake flips freshness without any cluster involvement.
	a.observed.SetFresh()
	status, body = a.do("GET", "/v1/environments/"+envID+"/status", token, nil)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "fresh", body["observation"].(map[string]any)["state"])

	// Observed nodes surface as deduplicated sorted platforms.
	a.observed.SetNodeArch("node-a", "amd64")
	status, body = a.do("GET", "/v1/environments/"+envID+"/status", token, nil)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, []any{"linux/amd64"}, body["platforms"])

	// Unknown environments are 404, malformed ids too.
	status, _ = a.do("GET", "/v1/environments/"+uuid.NewString()+"/status", token, nil)
	require.Equal(t, http.StatusNotFound, status)
	status, _ = a.do("GET", "/v1/environments/not-a-uuid/status", token, nil)
	require.Equal(t, http.StatusNotFound, status)

	// Unauthenticated reads are rejected.
	status, _ = a.do("GET", "/v1/environments/"+envID+"/status", "", nil)
	require.Equal(t, http.StatusUnauthorized, status)
}

func TestSystemObservation(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("system@example.com", "hunter2hunter2")
	token := a.login("system@example.com", "hunter2hunter2")

	status, body := a.do("GET", "/v1/system/observation", token, nil)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "api-only", body["mode"], "the test kernel has no cluster")
	require.Equal(t, false, body["ready"])
	require.Equal(t, "unknown", body["observation"].(map[string]any)["state"])
}

// The status stream sends the current document immediately and again on
// every invalidation; it lives outside the request timeout group.
func TestEnvironmentStatusStream(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("stream@example.com", "hunter2hunter2")
	token := a.login("stream@example.com", "hunter2hunter2")
	_, envID := a.createEnvironment(t, token)
	environmentID := uuid.MustParse(envID)
	a.observed.SetFresh()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", a.srv.URL+"/v1/environments/"+envID+"/status/stream", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := a.srv.Client().Do(req)
	require.NoError(t, err)
	defer res.Body.Close()
	require.Equal(t, http.StatusOK, res.StatusCode)
	require.Equal(t, "text/event-stream", res.Header.Get("Content-Type"))

	reader := bufio.NewReader(res.Body)
	readStatusEvent := func() string {
		t.Helper()
		data := make(chan string, 1)
		go func() {
			for {
				line, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				if strings.HasPrefix(line, "data: ") {
					data <- strings.TrimRight(strings.TrimPrefix(line, "data: "), "\n")
					return
				}
			}
		}()
		select {
		case payload := <-data:
			return payload
		case <-time.After(5 * time.Second):
			t.Fatal("no status event received")
			return ""
		}
	}

	first := readStatusEvent()
	require.Contains(t, first, envID)
	require.Contains(t, first, `"state":"fresh"`)

	// An observation change for this environment triggers a re-send.
	a.observed.SetWorkload(environmentID, "skali-demo-production", "demo-web", "web", "abcd",
		module.WorkloadStatus{Desired: 2, Ready: 1, Updated: 2})
	second := readStatusEvent()
	require.Contains(t, second, envID)
}
