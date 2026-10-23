package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// The access matrix: every classified route is probed with one fixture per
// rung of the ladder and must answer 404 (invisible), 403 (role too low), or
// anything but those (allowed) exactly as its class says. The fixture is
// one project with one deployed environment and every entity a route can
// address, seeded once through the real API.

type matrixFixture struct {
	a          *testAPI
	tokens     map[string]string
	project    string
	env        string
	run        string
	step       string
	deployment string
	revision   string
	artifact   string
	build      string
}

func newMatrixFixture(t *testing.T) *matrixFixture {
	t.Helper()
	a := newTestAPI(t)
	f := &matrixFixture{a: a, tokens: map[string]string{}}
	const password = "hunter2hunter2"
	a.createAdmin("root@example.com", password)
	a.createUser("owner@example.com", password)
	for _, name := range []string{"maintainer", "deployer", "reader", "locked", "stranger"} {
		a.createMember(name+"@example.com", password)
	}
	for _, name := range []string{"root", "owner", "maintainer", "deployer", "reader", "locked", "stranger"} {
		f.tokens[name] = a.login(name+"@example.com", password)
	}
	owner := f.tokens["owner"]
	f.project, f.env = a.createEnvironment(t, owner)
	a.grantMember(t, f.project, "maintainer@example.com", "maintain")
	a.grantMember(t, f.project, "deployer@example.com", "deploy")
	a.grantMember(t, f.project, "reader@example.com", "read")
	a.grantMember(t, f.project, "locked@example.com", "read")
	a.setCell(t, f.env, "locked@example.com", "none")

	// One full deployment: open (artifact, build, run, deployment), a client
	// step owned by the actor, verify, complete, activate (revision).
	definitionVersion := a.submitDefinition(t, owner, f.project, deployAPIManifest)
	candidate := a.stageValues(t, owner, f.env, definitionVersion, "matrix-secret-value")
	status, body := a.do("POST", "/v1/environments/"+f.env+"/deployments", owner, map[string]any{
		"definition_version_id": definitionVersion,
		"candidate_id":          candidate,
		"builds":                buildsPayload(),
	})
	require.Equal(t, http.StatusCreated, status, "%v", body)
	deployment := body["deployment"].(map[string]any)
	f.deployment = deployment["id"].(string)
	f.run = deployment["run_id"].(string)
	for _, raw := range body["actions"].([]any) {
		action := raw.(map[string]any)
		if action["action"].(string) != "build" {
			continue
		}
		f.artifact = action["artifact_id"].(string)
		f.build = action["build_id"].(string)
	}
	require.NotEmpty(t, f.artifact)
	require.NotEmpty(t, f.build)
	status, body = a.do("POST", "/v1/runs/"+f.run+"/steps", owner, map[string]any{
		"key": "artifacts.web", "title": "web", "parent_key": "artifacts",
	})
	require.Equal(t, http.StatusOK, status, "%v", body)
	f.step = body["id"].(string)
	a.registryHolds("skali/demo/web", webDigest)
	status, body = a.do("POST", "/v1/artifacts/"+f.artifact+"/verify", owner, map[string]any{
		"deployment_id": f.deployment, "digest": webDigest,
	})
	require.Equal(t, http.StatusOK, status, "%v", body)
	status, body = a.do("POST", "/v1/deployments/"+f.deployment+"/complete", owner, nil)
	require.Equal(t, http.StatusAccepted, status, "%v", body)
	completed := a.awaitDeployment(t, owner, f.deployment)
	require.Equal(t, "promoted", completed["status"], "%v", completed)
	f.revision = completed["revision_id"].(string)
	a.finishRun(t, f.run)
	a.activate(t, f.env)
	return f
}

// path fills a route template with fixture ids.
func (f *matrixFixture) path(template string) string {
	id := uuid.NewString()
	switch {
	case strings.HasPrefix(template, "/v1/projects/{id}"):
		id = f.project
	case strings.HasPrefix(template, "/v1/environments/{id}"):
		id = f.env
	case strings.HasPrefix(template, "/v1/runs/{id}"):
		id = f.run
	case strings.HasPrefix(template, "/v1/steps/{id}"):
		id = f.step
	case strings.HasPrefix(template, "/v1/deployments/{id}"):
		id = f.deployment
	case strings.HasPrefix(template, "/v1/revisions/{id}"):
		id = f.revision
	case strings.HasPrefix(template, "/v1/artifacts/{id}"):
		id = f.artifact
	case strings.HasPrefix(template, "/v1/builds/{id}"):
		id = f.build
	}
	r := strings.NewReplacer("{id}", id, "{key}", "web", "{name}", "SESSION_SECRET", "{user}", uuid.NewString(), "{snapshot}", uuid.NewString())
	return r.Replace(template)
}

// statusStreaming stands in for a stream that was let through: the handler
// held the connection open instead of refusing it.
const statusStreaming = -1

// probeDeadlines bound one probe by what the route can do. A stream the
// handler lets through holds the connection open, so its probe is short
// and running out reads as statusStreaming. A finite handler always
// answers; its deadline only catches one that never does, so it leaves
// room for the slowest of them on a loaded CI runner: enabling 2FA hashes
// ten backup codes in sequence with the password Argon2id parameters
// (64 MiB, 3 passes), about 400 ms each there.
type probeDeadlines struct {
	stream time.Duration
	finite time.Duration
}

var matrixDeadlines = probeDeadlines{stream: 2 * time.Second, finite: 30 * time.Second}

// probe sends one request and returns the status and, for finite answers,
// the decoded body. stream marks a probe that expects to be let into a
// stream; every other probe must answer within the finite deadline.
func (f *matrixFixture) probe(t *testing.T, method, path, token string, stream bool) (int, map[string]any) {
	t.Helper()
	status, body, err := sendProbe(f.a.srv.Client(), method, f.a.srv.URL+path, token, stream, matrixDeadlines)
	require.NoError(t, err, "%s %s", method, path)
	return status, body
}

// sendProbe is probe without the test: a stream that neither refuses nor
// answers within the stream deadline reports statusStreaming, and a finite
// request that does not answer within the finite deadline is an error.
func sendProbe(client *http.Client, method, url, token string, stream bool, deadlines probeDeadlines) (int, map[string]any, error) {
	deadline := deadlines.finite
	if stream {
		deadline = deadlines.stream
	}
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := client.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			if stream {
				return statusStreaming, nil, nil
			}
			return 0, nil, fmt.Errorf("no answer within %s: %w", deadline, err)
		}
		return 0, nil, err
	}
	defer res.Body.Close()
	if stream {
		return res.StatusCode, nil, nil
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return 0, nil, err
	}
	var body map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &body)
	}
	return res.StatusCode, body, nil
}

// expectation names, per access class, the fixtures that must be refused
// with 404, those refused with 403, and those let through.
type expectation struct {
	notFound  []string
	forbidden []string
	allowed   []string
}

var matrixExpectations = map[string]expectation{
	"public":         {},
	"self":           {allowed: []string{"stranger"}},
	"instance-admin": {forbidden: []string{"reader", "owner"}, allowed: []string{"root"}},
	"project:create": {forbidden: []string{"stranger"}, allowed: []string{"owner", "root"}},
	"project:list":   {allowed: []string{"stranger", "reader"}},

	"project:read":     {notFound: []string{"stranger"}, allowed: []string{"reader", "locked", "owner", "root"}},
	"project:maintain": {notFound: []string{"stranger"}, forbidden: []string{"reader", "deployer", "locked"}, allowed: []string{"maintainer", "owner", "root"}},
	"project:admin":    {notFound: []string{"stranger"}, forbidden: []string{"maintainer", "locked"}, allowed: []string{"owner", "root"}},
	"project:deployer": {notFound: []string{"stranger"}, forbidden: []string{"reader", "locked"}, allowed: []string{"deployer", "maintainer", "owner", "root"}},

	"environment:none":     {notFound: []string{"stranger"}, allowed: []string{"locked", "reader", "root"}},
	"environment:read":     {notFound: []string{"stranger"}, forbidden: []string{"locked"}, allowed: []string{"reader", "deployer", "root"}},
	"environment:deploy":   {notFound: []string{"stranger"}, forbidden: []string{"reader", "locked"}, allowed: []string{"deployer", "owner", "root"}},
	"environment:maintain": {notFound: []string{"stranger"}, forbidden: []string{"deployer", "locked"}, allowed: []string{"maintainer", "owner", "root"}},
	"environment:admin":    {notFound: []string{"stranger"}, forbidden: []string{"maintainer", "locked"}, allowed: []string{"owner", "root"}},

	"revision:read":     {notFound: []string{"stranger"}, forbidden: []string{"locked"}, allowed: []string{"reader", "root"}},
	"deployment:read":   {notFound: []string{"stranger"}, forbidden: []string{"locked"}, allowed: []string{"reader", "root"}},
	"deployment:deploy": {notFound: []string{"stranger"}, forbidden: []string{"reader", "locked"}, allowed: []string{"deployer", "owner", "root"}},
	"run:read":          {notFound: []string{"stranger"}, forbidden: []string{"locked"}, allowed: []string{"reader", "root"}},
	"run:deploy":        {notFound: []string{"stranger"}, forbidden: []string{"reader", "locked"}, allowed: []string{"owner", "root"}},
	"step:read":         {notFound: []string{"stranger"}, forbidden: []string{"locked"}, allowed: []string{"reader", "root"}},
	"step:deploy":       {notFound: []string{"stranger"}, forbidden: []string{"reader", "locked"}, allowed: []string{"owner", "root"}},
	"artifact:deployer": {notFound: []string{"stranger"}, forbidden: []string{"reader", "locked"}, allowed: []string{"deployer", "owner", "root"}},
	"build:deployer":    {notFound: []string{"stranger"}, forbidden: []string{"reader", "locked"}, allowed: []string{"deployer", "owner", "root"}},
}

// notFoundAllowed are reads whose fixture legitimately lacks the addressed
// thing (no draft, no claim, no backup target); 404 is their success.
var notFoundAllowed = map[string]bool{
	"GET /v1/projects/{id}/draft":                          true,
	"GET /v1/environments/{id}/databases/{key}/connection": true,
	"GET /v1/environments/{id}/buckets/{key}/connection":   true,
	"GET /v1/system/backup-target":                         true,
	"GET /v1/system/backup-target/summary":                 true,
	"GET /v1/auth/device/codes/{user_code}":                true,
	"GET /v1/system/database-pools/{name}":                 true,
	"GET /v1/system/database-pools/{name}/metrics":         true,
}

// destructiveRoutes would change the fixture when let through; their allowed
// side is covered by focused tests.
var destructiveRoutes = map[string]bool{
	"DELETE /v1/projects/{id}":                                      true,
	"DELETE /v1/environments/{id}":                                  true,
	"POST /v1/environments/{id}/teardown":                           true,
	"DELETE /v1/auth/sessions/{id}":                                 true,
	"POST /v1/auth/logout":                                          true,
	"DELETE /v1/system/backup-target":                               true,
	"DELETE /v1/environments/{id}/values/{name}":                    true,
	"POST /v1/environments/{id}/buckets/{key}/credentials/rotate":   true,
	"POST /v1/environments/{id}/databases/{key}/credentials/rotate": true,
}

func TestAccessMatrix(t *testing.T) {
	f := newMatrixFixture(t)

	routes := make([]string, 0, len(f.a.access.classes))
	for route := range f.a.access.classes {
		routes = append(routes, route)
	}
	sort.Strings(routes)
	for _, route := range routes {
		class := f.a.access.classes[route]
		expect, ok := matrixExpectations[class.name]
		require.True(t, ok, "no matrix expectation for class %q (route %s)", class.name, route)
		method, template, _ := strings.Cut(route, " ")
		if !strings.HasPrefix(template, "/v1/") {
			continue
		}
		path := f.path(template)
		stream := strings.HasSuffix(template, "/stream")
		t.Run(route, func(t *testing.T) {
			for _, name := range expect.notFound {
				status, body := f.probe(t, method, path, f.tokens[name], false)
				require.Equal(t, http.StatusNotFound, status, "%s as %s: %v", route, name, body)
				require.Equal(t, "not_found", errorCode(t, body), "%s as %s", route, name)
			}
			for _, name := range expect.forbidden {
				status, body := f.probe(t, method, path, f.tokens[name], false)
				require.Equal(t, http.StatusForbidden, status, "%s as %s: %v", route, name, body)
				require.Equal(t, "forbidden", errorCode(t, body), "%s as %s", route, name)
			}
			if destructiveRoutes[route] {
				return
			}
			for _, name := range expect.allowed {
				status, body := f.probe(t, method, path, f.tokens[name], stream)
				require.NotEqual(t, http.StatusUnauthorized, status, "%s as %s: %v", route, name, body)
				require.NotEqual(t, http.StatusForbidden, status, "%s as %s: %v", route, name, body)
				if method == http.MethodGet && !notFoundAllowed[route] {
					require.NotEqual(t, http.StatusNotFound, status, "%s as %s: %v", route, name, body)
				}
			}
		})
	}
}

// A finite handler slower than the stream deadline still answers with its
// own status and body instead of passing for an accepted stream; only a
// stream probe reads a deadline as statusStreaming, and a finite handler
// that never answers fails the probe.
func TestProbeTellsSlowAnswersFromStreams(t *testing.T) {
	arrived := make(chan struct{})
	proceed := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/slow":
			close(arrived)
			<-proceed
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"ok":true}`)
		case "/hang":
			<-r.Context().Done()
		}
	}))
	defer srv.Close()
	deadlines := probeDeadlines{stream: 50 * time.Millisecond, finite: 30 * time.Second}

	type answer struct {
		status int
		body   map[string]any
		err    error
	}
	slow := make(chan answer, 1)
	go func() {
		status, body, err := sendProbe(srv.Client(), http.MethodPost, srv.URL+"/slow", "token", false, deadlines)
		slow <- answer{status, body, err}
	}()
	<-arrived
	// The slow request is in flight for the whole stream deadline this
	// stream probe runs out, and only answers after it.
	status, body, err := sendProbe(srv.Client(), http.MethodGet, srv.URL+"/hang", "token", true, deadlines)
	require.NoError(t, err)
	require.Equal(t, statusStreaming, status)
	require.Nil(t, body)
	close(proceed)

	got := <-slow
	require.NoError(t, got.err)
	require.Equal(t, http.StatusCreated, got.status)
	require.Equal(t, map[string]any{"ok": true}, got.body)

	// A finite route that never answers is a failure, not a stream.
	_, _, err = sendProbe(srv.Client(), http.MethodGet, srv.URL+"/hang", "token", false,
		probeDeadlines{stream: time.Minute, finite: 50 * time.Millisecond})
	require.ErrorIs(t, err, context.DeadlineExceeded)
}
