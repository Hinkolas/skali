package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
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
	require.Equal(t, http.StatusOK, status, "%v", body)
	f.revision = body["revision_id"].(string)
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
	r := strings.NewReplacer("{id}", id, "{key}", "web", "{name}", "SESSION_SECRET", "{user}", uuid.NewString())
	return r.Replace(template)
}

// statusStreaming stands in for a stream that was let through: the handler
// held the connection open instead of refusing it.
const statusStreaming = -1

// probe sends one request and returns the status and, for non-streaming
// answers, the decoded body. A stream that neither refuses nor answers
// within the deadline reports statusStreaming.
func (f *matrixFixture) probe(t *testing.T, method, path, token string, readBody bool) (int, map[string]any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, f.a.srv.URL+path, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := f.a.srv.Client().Do(req)
	if err != nil {
		if !readBody && errors.Is(err, context.DeadlineExceeded) {
			return statusStreaming, nil
		}
		require.NoError(t, err)
	}
	defer res.Body.Close()
	if !readBody {
		return res.StatusCode, nil
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	require.NoError(t, err)
	var body map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &body)
	}
	return res.StatusCode, body
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
	"GET /v1/auth/device/codes/{user_code}":                true,
}

// destructiveRoutes would change the fixture when let through; their allowed
// side is covered by focused tests.
var destructiveRoutes = map[string]bool{
	"DELETE /v1/projects/{id}":                   true,
	"DELETE /v1/environments/{id}":               true,
	"POST /v1/environments/{id}/teardown":        true,
	"DELETE /v1/auth/sessions/{id}":              true,
	"POST /v1/auth/logout":                       true,
	"DELETE /v1/system/backup-target":            true,
	"DELETE /v1/environments/{id}/values/{name}": true,
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
				status, body := f.probe(t, method, path, f.tokens[name], true)
				require.Equal(t, http.StatusNotFound, status, "%s as %s: %v", route, name, body)
				require.Equal(t, "not_found", errorCode(t, body), "%s as %s", route, name)
			}
			for _, name := range expect.forbidden {
				status, body := f.probe(t, method, path, f.tokens[name], true)
				require.Equal(t, http.StatusForbidden, status, "%s as %s: %v", route, name, body)
				require.Equal(t, "forbidden", errorCode(t, body), "%s as %s", route, name)
			}
			if destructiveRoutes[route] {
				return
			}
			for _, name := range expect.allowed {
				status, body := f.probe(t, method, path, f.tokens[name], !stream)
				require.NotEqual(t, http.StatusUnauthorized, status, "%s as %s: %v", route, name, body)
				require.NotEqual(t, http.StatusForbidden, status, "%s as %s: %v", route, name, body)
				if method == http.MethodGet && !notFoundAllowed[route] {
					require.NotEqual(t, http.StatusNotFound, status, "%s as %s: %v", route, name, body)
				}
			}
		})
	}
}
