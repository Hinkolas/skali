package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/checkout"
	"github.com/Hinkolas/skali/internal/cliconfig"
	"github.com/Hinkolas/skali/internal/client"
	"github.com/Hinkolas/skali/internal/compiler"
)

func writeFile(t *testing.T, root, name, content string) string {
	t.Helper()
	path := filepath.Join(root, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

const flowManifest = `version: "1"
name: flowdemo
applications:
  web:
    build:
      context: .
    ports:
      http:
        port: 8080
        protocol: http
    routes:
      public:
        domain: "${APP_DOMAIN}"
        port: http
`

func TestLoadLocalProjectAndBuildInputs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "skali.yml", flowManifest)
	writeFile(t, root, "Dockerfile", "FROM scratch\nCOPY main.txt /\n")
	writeFile(t, root, "main.txt", "content")
	envFile := writeFile(t, root, ".env", "APP_DOMAIN=flow.localhost\n")

	project, err := loadLocalProject(filepath.Join(root, "skali.yml"))
	require.NoError(t, err)
	require.Equal(t, "flowdemo", project.Result.Definition.Name)
	require.Equal(t, root, project.Root)

	inputs, contexts, err := buildInputs(project, []string{envFile}, map[string]string{"web": "linux/amd64"}, nil)
	require.NoError(t, err)
	require.Contains(t, inputs, "web")
	require.Len(t, inputs["web"].InputHash, 64)
	require.Equal(t, "linux/amd64", inputs["web"].Platform)
	// The env file never enters the context inventory.
	require.NotContains(t, contexts["web"].Files, ".env")

	// The platform is part of the dedup key: a different target rebuilds.
	otherPlatform, _, err := buildInputs(project, []string{envFile}, map[string]string{"web": "linux/arm64"}, nil)
	require.NoError(t, err)
	require.NotEqual(t, inputs["web"].InputHash, otherPlatform["web"].InputHash)

	// A source change moves the input hash; the dedup key is honest.
	writeFile(t, root, "main.txt", "changed")
	changed, _, err := buildInputs(project, []string{envFile}, map[string]string{"web": "linux/amd64"}, nil)
	require.NoError(t, err)
	require.NotEqual(t, inputs["web"].InputHash, changed["web"].InputHash)

	// A host-run intercept is skipped from the build inputs entirely.
	skipped, _, err := buildInputs(project, []string{envFile}, map[string]string{"web": "linux/amd64"},
		map[string]client.LocalApplication{"web": {Ports: map[string]int{"http": 5173}}})
	require.NoError(t, err)
	require.NotContains(t, skipped, "web")
}

func platformDefinition(apps map[string]compiler.ApplicationSource) compiler.ProjectDefinition {
	definition := compiler.ProjectDefinition{Applications: map[string]compiler.Application{}}
	for key, source := range apps {
		definition.Applications[key] = compiler.Application{Source: source}
	}
	return definition
}

func TestResolveAppPlatforms(t *testing.T) {
	local := "linux/" + runtime.GOARCH
	buildApp := compiler.ApplicationSource{Kind: "build"}
	definition := platformDefinition(map[string]compiler.ApplicationSource{"web": buildApp})

	// No status (old server or failed fetch): host arch plus a notice.
	var out strings.Builder
	platforms, err := resolveAppPlatforms(&out, nil, "", definition, nil)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"web": local}, platforms)
	require.Contains(t, out.String(), "did not report")

	// An empty platform list is the same fallback.
	out.Reset()
	platforms, err = resolveAppPlatforms(&out, &client.EnvironmentStatus{}, "", definition, nil)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"web": local}, platforms)
	require.Contains(t, out.String(), "did not report")

	// A reported platform wins; a foreign one is announced.
	out.Reset()
	status := &client.EnvironmentStatus{Platforms: []string{"linux/amd64"}}
	platforms, err = resolveAppPlatforms(&out, status, "", definition, nil)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"web": "linux/amd64"}, platforms)
	if local != "linux/amd64" {
		require.Contains(t, out.String(), "cluster architecture")
	}

	// A matching single platform stays silent.
	out.Reset()
	status = &client.EnvironmentStatus{Platforms: []string{local}}
	platforms, err = resolveAppPlatforms(&out, status, "", definition, nil)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"web": local}, platforms)
	require.Empty(t, out.String())

	// Mixed clusters join into one sorted multi-platform build.
	status = &client.EnvironmentStatus{Platforms: []string{"linux/arm64", "linux/amd64"}}
	platforms, err = resolveAppPlatforms(io.Discard, status, "", definition, nil)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"web": "linux/amd64,linux/arm64"}, platforms)

	// An override beats the report and is canonicalized.
	out.Reset()
	status = &client.EnvironmentStatus{Platforms: []string{"linux/amd64"}}
	platforms, err = resolveAppPlatforms(&out, status, "linux/amd64", definition, nil)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"web": "linux/amd64"}, platforms)
	require.Contains(t, out.String(), "override")
	platforms, err = resolveAppPlatforms(io.Discard, nil, " linux/arm64, linux/amd64 ,linux/arm64", definition, nil)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"web": "linux/amd64,linux/arm64"}, platforms)

	// An override outside an application's buildable platforms names it.
	status = &client.EnvironmentStatus{Platforms: []string{"linux/amd64"}}
	_, err = resolveAppPlatforms(io.Discard, status, "linux/arm64", definition, nil)
	require.ErrorContains(t, err, "web")
	require.ErrorContains(t, err, "linux/arm64")
}

func TestResolveAppPlatformsDeclared(t *testing.T) {
	amdOnly := compiler.ApplicationSource{Kind: "build", Platforms: []string{"linux/amd64"}}
	portable := compiler.ApplicationSource{Kind: "build"}
	both := []string{"linux/amd64", "linux/arm64"}

	// Declared platforms narrow the candidates on a mixed cluster.
	definition := platformDefinition(map[string]compiler.ApplicationSource{"api": amdOnly, "web": portable})
	status := &client.EnvironmentStatus{Platforms: both}
	platforms, err := resolveAppPlatforms(io.Discard, status, "", definition, nil)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"api": "linux/amd64", "web": "linux/amd64,linux/arm64"}, platforms)

	// Declared platforms disjoint from the cluster fail before the plan.
	status = &client.EnvironmentStatus{Platforms: []string{"linux/arm64"}}
	_, err = resolveAppPlatforms(io.Discard, status, "", definition, nil)
	require.ErrorContains(t, err, "api")
	require.ErrorContains(t, err, "linux/arm64")

	// The same check guards image-sourced applications, which build nothing.
	imageDefinition := platformDefinition(map[string]compiler.ApplicationSource{
		"worker": {Kind: "image", Image: "example.invalid/worker:1", Platforms: []string{"linux/amd64"}},
	})
	_, err = resolveAppPlatforms(io.Discard, status, "", imageDefinition, nil)
	require.ErrorContains(t, err, "worker")
	platforms, err = resolveAppPlatforms(io.Discard, &client.EnvironmentStatus{Platforms: both}, "", imageDefinition, nil)
	require.NoError(t, err)
	require.Empty(t, platforms, "image applications produce no build platform")

	// Without a status, declared platforms stand in for the local arch.
	platforms, err = resolveAppPlatforms(io.Discard, nil, "", definition, nil)
	require.NoError(t, err)
	require.Equal(t, "linux/amd64", platforms["api"])
}

func TestResolveAppPlatformsPreference(t *testing.T) {
	buildApp := compiler.ApplicationSource{Kind: "build"}
	amdOnly := compiler.ApplicationSource{Kind: "build", Platforms: []string{"linux/amd64"}}
	definition := platformDefinition(map[string]compiler.ApplicationSource{"web": buildApp, "legacy": amdOnly})
	status := &client.EnvironmentStatus{
		Platforms:          []string{"linux/amd64", "linux/arm64"},
		PlatformPreference: []string{"linux/arm64"},
	}

	// The preference picks a single arch for portable applications while a
	// declared-subset application falls through to its own candidates.
	platforms, err := resolveAppPlatforms(io.Discard, status, "", definition, nil)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"web": "linux/arm64", "legacy": "linux/amd64"}, platforms)

	// A preference matching no candidate falls back to the full set.
	status.PlatformPreference = []string{"linux/riscv64"}
	platforms, err = resolveAppPlatforms(io.Discard, status, "", definition, nil)
	require.NoError(t, err)
	require.Equal(t, "linux/amd64,linux/arm64", platforms["web"])

	// Ordered preference: the first entry present in the candidates wins.
	status.PlatformPreference = []string{"linux/riscv64", "linux/amd64", "linux/arm64"}
	platforms, err = resolveAppPlatforms(io.Discard, status, "", definition, nil)
	require.NoError(t, err)
	require.Equal(t, "linux/amd64", platforms["web"])
}

func TestDiscoverEnvFiles(t *testing.T) {
	root := t.TempDir()
	require.Empty(t, discoverEnvFiles(root))
	writeFile(t, root, "notes.txt", "x")
	writeFile(t, root, "production.env", "A=1\n") // old convention, not discovered
	staging := writeFile(t, root, ".env.staging", "A=1\n")
	generic := writeFile(t, root, ".env", "A=1\n")
	production := writeFile(t, root, ".env.production", "A=1\n")
	require.Equal(t, []string{generic, production, staging}, discoverEnvFiles(root))
}

func TestChooseEnvFile(t *testing.T) {
	root := t.TempDir()
	var out strings.Builder

	// No env files: silently keep the stored values, no prompt printed.
	selected, err := chooseEnvFile(&out, bufio.NewReader(strings.NewReader("")), root, "production")
	require.NoError(t, err)
	require.Empty(t, selected)
	require.Empty(t, out.String())

	writeFile(t, root, ".env", "A=1\n")
	production := writeFile(t, root, ".env.production", "A=1\n")

	selected, err = chooseEnvFile(&out, bufio.NewReader(strings.NewReader("3\n")), root, "production")
	require.NoError(t, err)
	require.Equal(t, production, selected)
	require.Contains(t, out.String(), "Override production with a local env file?")
	require.Contains(t, out.String(), "  1) Use stored values\n")

	// Empty input takes the default: keep the stored values.
	selected, err = chooseEnvFile(&out, bufio.NewReader(strings.NewReader("\n")), root, "production")
	require.NoError(t, err)
	require.Empty(t, selected)
}

func TestChooseEnvironment(t *testing.T) {
	environments := []client.Environment{
		{ID: "e1", Name: "production"},
		{ID: "e2", Name: "staging"},
	}

	var out strings.Builder
	name, err := chooseEnvironment(&out, bufio.NewReader(strings.NewReader("2\n")), environments)
	require.NoError(t, err)
	require.Equal(t, "staging", name)
	require.Contains(t, out.String(), "Which environment should Skali use?:\n")
	require.Contains(t, out.String(), "  2) staging\n")

	// A single environment selects itself without a prompt.
	out.Reset()
	name, err = chooseEnvironment(&out, bufio.NewReader(strings.NewReader("")), environments[:1])
	require.NoError(t, err)
	require.Equal(t, "production", name)
	require.Empty(t, out.String())
}

func TestSameMaster(t *testing.T) {
	require.True(t, sameMaster("https://skali.example.com", "https://skali.example.com"))
	require.True(t, sameMaster("https://skali.example.com/", "https://skali.example.com"))
	require.True(t, sameMaster("https://SKALI.example.com", "https://skali.example.com"))
	require.False(t, sameMaster("http://skali.example.com", "https://skali.example.com"))
	require.False(t, sameMaster("https://skali.example.com:8443", "https://skali.example.com"))
	require.False(t, sameMaster("https://other.example.com", "https://skali.example.com"))
}

func TestLookupRemoteByMaster(t *testing.T) {
	cfg := &cliconfig.Config{Remotes: map[string]*cliconfig.Remote{
		"beta":  {Master: "https://skali.example.com"},
		"alpha": {Master: "https://skali.example.com"},
		"other": {Master: "https://other.example.com"},
	}}
	name, remote, ok := lookupRemoteByMaster(cfg, "https://skali.example.com/")
	require.True(t, ok)
	require.Equal(t, "alpha", name) // duplicates resolve in sorted name order
	require.Equal(t, "https://skali.example.com", remote.Master)

	_, _, ok = lookupRemoteByMaster(cfg, "https://unknown.example.com")
	require.False(t, ok)
}

// fakeInstall is a minimal stateful projects/environments/access API for
// target resolution and management command tests, recording writes in
// posts ("project:name", "environment:p1:name", "member:p1:bob:read",
// "cell:p1-e1:bob:deploy", "settings:p1-e1", "teardown:p1-e1:purge", ...).
type fakeInstall struct {
	mu       sync.Mutex
	projects []client.Project
	envs     map[string][]client.Environment
	backups  map[string][]client.BackupSnapshot
	members  map[string][]client.Member
	posts    []string
	// reauthRequired makes every gated write answer reauth_required until
	// the session reauthenticates once; reauths counts those calls.
	reauthRequired bool
	reauths        int
	twoFactor      bool
	srv            *httptest.Server
}

func newFakeInstall(t *testing.T) *fakeInstall {
	t.Helper()
	f := &fakeInstall{
		envs:    map[string][]client.Environment{},
		backups: map[string][]client.BackupSnapshot{},
		members: map[string][]client.Member{},
	}
	writeError := func(w http.ResponseWriter, status int, code, message string) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": message}})
	}
	// gate answers reauth_required once when the fixture demands it.
	gate := func(w http.ResponseWriter) bool {
		if f.reauthRequired {
			writeError(w, http.StatusForbidden, "reauth_required", "recent authentication required")
			return false
		}
		return true
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/auth/session", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(client.SessionInfo{User: client.User{Email: "me@example.com", TwoFactorEnabled: f.twoFactor}})
	})
	mux.HandleFunc("/v1/auth/reauth", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var req map[string]string
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req["password"] == "" && req["code"] == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "password or code is required")
			return
		}
		f.reauths++
		f.reauthRequired = false
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/v1/projects", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Method == http.MethodPost {
			var req struct {
				Name string `json:"name"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			created := client.Project{ID: fmt.Sprintf("p%d", len(f.projects)+1), Name: req.Name,
				Access: client.ProjectAccess{Role: "admin", Environments: map[string]string{}}}
			f.projects = append(f.projects, created)
			f.posts = append(f.posts, "project:"+req.Name)
			_ = json.NewEncoder(w).Encode(map[string]any{"project": created})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"projects": f.projects})
	})
	mux.HandleFunc("/v1/projects/", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/projects/"), "/")
		projectID := parts[0]
		sub := ""
		if len(parts) > 1 {
			sub = parts[1]
		}
		switch {
		case sub == "backups":
			snapshots := f.backups[projectID]
			if snapshots == nil {
				snapshots = []client.BackupSnapshot{}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"snapshots": snapshots})
		case sub == "members" && len(parts) == 2:
			members := f.members[projectID]
			if members == nil {
				members = []client.Member{}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"members": members})
		case sub == "members" && len(parts) == 3:
			if !gate(w) {
				return
			}
			user, _ := url.PathUnescape(parts[2])
			if r.Method == http.MethodDelete {
				before := len(f.members[projectID])
				f.members[projectID] = slices.DeleteFunc(f.members[projectID], func(m client.Member) bool {
					return m.Email == user
				})
				if len(f.members[projectID]) == before {
					writeError(w, http.StatusNotFound, "not_found", "not a member")
					return
				}
				f.posts = append(f.posts, "member-rm:"+projectID+":"+user)
				w.WriteHeader(http.StatusNoContent)
				return
			}
			var req struct {
				Role string `json:"role"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			member := client.Member{UserID: "u-" + user, Email: user, Role: req.Role}
			f.members[projectID] = append(f.members[projectID], member)
			f.posts = append(f.posts, "member:"+projectID+":"+user+":"+req.Role)
			_ = json.NewEncoder(w).Encode(map[string]any{"member": member})
		case sub == "environments" && r.Method == http.MethodPost:
			var req struct {
				Name     string `json:"name"`
				Priority string `json:"priority"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			created := newFakeEnvironment(fmt.Sprintf("e%d", len(f.envs[projectID])+1), projectID, req.Name)
			if req.Priority == "high" {
				created.Settings.Priority, created.Settings.MaxRole = "high", "read"
			}
			f.envs[projectID] = append(f.envs[projectID], created)
			f.posts = append(f.posts, "environment:"+projectID+":"+req.Name)
			_ = json.NewEncoder(w).Encode(map[string]any{"environment": created})
		default:
			envs := f.envs[projectID]
			if envs == nil {
				envs = []client.Environment{}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"environments": envs})
		}
	})
	mux.HandleFunc("/v1/environments/", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/environments/"), "/")
		env := f.environment(parts[0])
		if env == nil {
			writeError(w, http.StatusNotFound, "not_found", "not found")
			return
		}
		sub := ""
		if len(parts) > 1 {
			sub = parts[1]
		}
		switch {
		case sub == "" && r.Method == http.MethodPatch:
			if !gate(w) {
				return
			}
			var patch client.EnvironmentSettingsPatch
			_ = json.NewDecoder(r.Body).Decode(&patch)
			if patch.MaxRole != nil {
				env.Settings.MaxRole = *patch.MaxRole
			}
			if patch.DeployPolicy != nil {
				env.Settings.DeployPolicy = *patch.DeployPolicy
			}
			if patch.PromoteFrom != nil {
				env.Settings.PromoteFrom = *patch.PromoteFrom
			}
			if patch.Priority != nil {
				env.Settings.Priority = *patch.Priority
			}
			f.posts = append(f.posts, "settings:"+env.ID)
			_ = json.NewEncoder(w).Encode(map[string]any{"environment": env})
		case sub == "":
			_ = json.NewEncoder(w).Encode(map[string]any{"environment": env})
		case sub == "backups" && r.Method == http.MethodPost:
			f.posts = append(f.posts, "backup:"+env.ID)
			_ = json.NewEncoder(w).Encode(map[string]any{"run_id": "run-backup-1", "backup_id": "snap-1"})
		case sub == "access" && len(parts) == 3:
			if !gate(w) {
				return
			}
			user, _ := url.PathUnescape(parts[2])
			if r.Method == http.MethodDelete {
				f.posts = append(f.posts, "cell-rm:"+env.ID+":"+user)
				w.WriteHeader(http.StatusNoContent)
				return
			}
			var req struct {
				Role string `json:"role"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			if !slices.ContainsFunc(f.members[env.ProjectID], func(m client.Member) bool { return m.Email == user }) {
				writeError(w, http.StatusConflict, "conflict", "user is not a member of the project")
				return
			}
			f.posts = append(f.posts, "cell:"+env.ID+":"+user+":"+req.Role)
			_ = json.NewEncoder(w).Encode(map[string]any{"access": client.Member{Email: user, Role: req.Role}})
		case sub == "teardown":
			if !gate(w) {
				return
			}
			var req map[string]bool
			_ = json.NewDecoder(r.Body).Decode(&req)
			f.posts = append(f.posts, fmt.Sprintf("teardown:%s:%v", env.ID, req["purge"]))
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{"run_id": "run-" + env.ID, "purge": req["purge"]})
		default:
			writeError(w, http.StatusNotFound, "not_found", "not found")
		}
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// environment finds an environment by id across projects (under f.mu).
func (f *fakeInstall) environment(id string) *client.Environment {
	for projectID := range f.envs {
		for i := range f.envs[projectID] {
			if f.envs[projectID][i].ID == id {
				return &f.envs[projectID][i]
			}
		}
	}
	return nil
}

// newFakeEnvironment is an open normal environment the caller administers.
func newFakeEnvironment(id, projectID, name string) client.Environment {
	return client.Environment{ID: id, ProjectID: projectID, Name: name, Access: "admin",
		Settings: &client.EnvironmentSettings{MaxRole: "admin", DeployPolicy: "direct", PromoteFrom: []string{}, Priority: "normal"}}
}

// seed installs a project with the given environments, all administered by
// the caller.
func (f *fakeInstall) seed(projectID, projectName string, envNames ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	access := client.ProjectAccess{Role: "admin", Environments: map[string]string{}}
	for _, name := range envNames {
		access.Environments[name] = "admin"
	}
	f.projects = append(f.projects, client.Project{ID: projectID, Name: projectName, Access: access})
	for i, name := range envNames {
		f.envs[projectID] = append(f.envs[projectID], newFakeEnvironment(fmt.Sprintf("%s-e%d", projectID, i+1), projectID, name))
	}
}

// testFlowProject compiles the flowdemo manifest in a fresh checkout root.
func testFlowProject(t *testing.T) *localProject {
	t.Helper()
	root := t.TempDir()
	writeFile(t, root, "skali.yml", flowManifest)
	project, err := loadLocalProject(filepath.Join(root, "skali.yml"))
	require.NoError(t, err)
	return project
}

// stageRemotes points the CLI config at a temp home with the given remotes.
func stageRemotes(t *testing.T, current string, remotes map[string]*cliconfig.Remote) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	require.NoError(t, cliconfig.Save(&cliconfig.Config{CurrentRemote: current, Remotes: remotes}))
}

func resolveForTest(t *testing.T, out *strings.Builder, in string, project *localProject,
	opts *deployOptions, planOnly, prompts bool) (*deployTarget, error) {
	t.Helper()
	return resolveDeployTarget(context.Background(), out, bufio.NewReader(strings.NewReader(in)),
		project, opts, planOnly, prompts)
}

func TestResolveDeployTargetBindingWinsOverCurrentRemote(t *testing.T) {
	current := newFakeInstall(t)
	bound := newFakeInstall(t)
	bound.seed("p1", "flowdemo", "production")
	stageRemotes(t, "current", map[string]*cliconfig.Remote{
		"current": {Master: current.srv.URL},
		"bound":   {Master: bound.srv.URL},
	})
	project := testFlowProject(t)
	require.NoError(t, checkout.Save(project.Root, &checkout.Target{
		Master: bound.srv.URL, Project: "flowdemo", Environment: "production"}))

	var out strings.Builder
	target, err := resolveForTest(t, &out, "", project, &deployOptions{UseBinding: true}, true, false)
	require.NoError(t, err)
	require.Equal(t, "bound", target.remoteName)
	require.Equal(t, bound.srv.URL, target.master)
	require.Equal(t, "p1-e1", target.environmentID)
	require.Contains(t, out.String(), "remote")
	require.Contains(t, out.String(), bound.srv.URL)
	require.Contains(t, out.String(), "environment  production\n")
	// The bound environment satisfied non-interactive use with no flag.
	require.NotContains(t, out.String(), "linked to remote")
}

func TestResolveDeployTargetUnknownBoundMaster(t *testing.T) {
	stageRemotes(t, "", map[string]*cliconfig.Remote{})
	project := testFlowProject(t)
	require.NoError(t, checkout.Save(project.Root, &checkout.Target{
		Master: "https://gone.example.com", Project: "flowdemo", Environment: "production"}))

	var out strings.Builder
	_, err := resolveForTest(t, &out, "", project, &deployOptions{UseBinding: true}, false, false)
	require.ErrorContains(t, err, "no remote for https://gone.example.com on this machine")
	require.ErrorContains(t, err, "skali remote add")
}

func TestResolveDeployTargetDriftGuard(t *testing.T) {
	install := newFakeInstall(t)
	stageRemotes(t, "r", map[string]*cliconfig.Remote{"r": {Master: install.srv.URL}})
	project := testFlowProject(t)
	require.NoError(t, checkout.Save(project.Root, &checkout.Target{
		Master: install.srv.URL, Project: "other-app", Environment: "production"}))

	var out strings.Builder
	_, err := resolveForTest(t, &out, "", project, &deployOptions{UseBinding: true}, false, false)
	require.ErrorContains(t, err, "linked to project other-app but the manifest names flowdemo")
	require.ErrorContains(t, err, "delete .skali/target.yaml to relink")
}

func TestResolveDeployTargetUnboundNonInteractiveNeedsEnvironment(t *testing.T) {
	install := newFakeInstall(t)
	install.seed("p1", "flowdemo", "production")
	stageRemotes(t, "r", map[string]*cliconfig.Remote{"r": {Master: install.srv.URL}})

	var out strings.Builder
	_, err := resolveForTest(t, &out, "", testFlowProject(t), &deployOptions{UseBinding: true}, false, false)
	require.ErrorContains(t, err, "--environment is required")
}

func TestResolveDeployTargetLinksOnceAndSkipsLocal(t *testing.T) {
	install := newFakeInstall(t)
	install.seed("p1", "flowdemo", "production")
	stageRemotes(t, "r", map[string]*cliconfig.Remote{"r": {Master: install.srv.URL}})
	project := testFlowProject(t)

	var out strings.Builder
	_, err := resolveForTest(t, &out, "", project,
		&deployOptions{UseBinding: true, Environment: "production"}, true, false)
	require.NoError(t, err)
	require.Contains(t, out.String(),
		"linked to remote r, project flowdemo, environment production; stored in .skali/")
	binding, err := checkout.Load(project.Root)
	require.NoError(t, err)
	require.Equal(t, &checkout.Target{Master: install.srv.URL, Project: "flowdemo",
		Environment: "production"}, binding)

	// A second run resolves through the binding and never rewrites it.
	out.Reset()
	_, err = resolveForTest(t, &out, "", project, &deployOptions{UseBinding: true}, true, false)
	require.NoError(t, err)
	require.NotContains(t, out.String(), "linked to remote")

	// The dev-owned local remote is never bound, even when the binding
	// machinery is on.
	stageRemotes(t, "", map[string]*cliconfig.Remote{"local": {Master: install.srv.URL}})
	local := testFlowProject(t)
	out.Reset()
	_, err = resolveForTest(t, &out, "", local,
		&deployOptions{Remote: "local", UseBinding: true, Environment: "production"}, true, false)
	require.NoError(t, err)
	require.NotContains(t, out.String(), "linked to remote")
	binding, err = checkout.Load(local.Root)
	require.NoError(t, err)
	require.Nil(t, binding)
}

func TestResolveDeployTargetExplicitRemoteBypassesCurrent(t *testing.T) {
	install := newFakeInstall(t)
	install.seed("p1", "flowdemo", "production")
	other := newFakeInstall(t)
	stageRemotes(t, "other", map[string]*cliconfig.Remote{
		"other": {Master: other.srv.URL},
		"local": {Master: install.srv.URL},
	})

	var out strings.Builder
	target, err := resolveForTest(t, &out, "", testFlowProject(t),
		&deployOptions{Remote: "local", Environment: "production"}, true, false)
	require.NoError(t, err)
	require.Equal(t, "local", target.remoteName)
	require.Equal(t, install.srv.URL, target.master)
}

func TestResolveDeployTargetLocalRemoteNeverCurrent(t *testing.T) {
	// A config from an older CLI where skali dev made the local platform
	// the current remote: an unbound deploy must block instead of silently
	// targeting it.
	install := newFakeInstall(t)
	install.seed("p1", "flowdemo", "production")
	stageRemotes(t, "local", map[string]*cliconfig.Remote{"local": {Master: install.srv.URL}})

	var out strings.Builder
	_, err := resolveForTest(t, &out, "", testFlowProject(t),
		&deployOptions{UseBinding: true, Environment: "production"}, false, false)
	require.ErrorContains(t, err, "no remote selected")
	require.ErrorContains(t, err, "skali remote add")
}

func TestResolveDeployTargetPlanNeverCreates(t *testing.T) {
	install := newFakeInstall(t)
	stageRemotes(t, "r", map[string]*cliconfig.Remote{"r": {Master: install.srv.URL}})

	var out strings.Builder
	_, err := resolveForTest(t, &out, "y\n", testFlowProject(t),
		&deployOptions{Environment: "production"}, true, true)
	require.ErrorContains(t, err, "project flowdemo does not exist on "+install.srv.URL)
	require.ErrorContains(t, err, "skali plan never changes the installation, run skali deploy to create it")

	install.seed("p1", "flowdemo")
	_, err = resolveForTest(t, &out, "y\n", testFlowProject(t),
		&deployOptions{Environment: "production"}, true, true)
	require.ErrorContains(t, err, "environment production does not exist in project flowdemo")
	require.ErrorContains(t, err, "skali plan never changes the installation")
	require.Empty(t, install.posts)
}

func TestResolveDeployTargetNonInteractiveNeverCreates(t *testing.T) {
	install := newFakeInstall(t)
	stageRemotes(t, "r", map[string]*cliconfig.Remote{"r": {Master: install.srv.URL}})

	var out strings.Builder
	_, err := resolveForTest(t, &out, "", testFlowProject(t),
		&deployOptions{Environment: "production"}, false, false)
	require.ErrorContains(t, err, "project flowdemo does not exist on "+install.srv.URL)
	require.ErrorContains(t, err, "run skali deploy interactively to create it")

	install.seed("p1", "flowdemo")
	_, err = resolveForTest(t, &out, "", testFlowProject(t),
		&deployOptions{Environment: "production"}, false, false)
	require.ErrorContains(t, err, "environment production does not exist in project flowdemo")
	require.ErrorContains(t, err, "run skali deploy interactively to create it")
	require.Empty(t, install.posts)
}

func TestResolveDeployTargetInteractiveCreateFlow(t *testing.T) {
	install := newFakeInstall(t)
	stageRemotes(t, "r", map[string]*cliconfig.Remote{"r": {Master: install.srv.URL}})
	project := testFlowProject(t)

	var out strings.Builder
	target, err := resolveForTest(t, &out, "y\n\ny\n", project,
		&deployOptions{UseBinding: true}, false, true)
	require.NoError(t, err)
	require.Equal(t, []string{"project:flowdemo", "environment:p1:production"}, install.posts)
	require.Equal(t, "p1", target.projectID)
	require.Equal(t, "e1", target.environmentID)
	require.Contains(t, out.String(), "Create project flowdemo on r? [y/N] ")
	require.Contains(t, out.String(), "Environment name [production]: ")
	require.Contains(t, out.String(), "Create environment production in project flowdemo? [y/N] ")
	require.Contains(t, out.String(),
		"linked to remote r, project flowdemo, environment production; stored in .skali/")
	binding, err := checkout.Load(project.Root)
	require.NoError(t, err)
	require.Equal(t, "production", binding.Environment)
}

func TestResolveDeployTargetDeclineAbortsBeforeCreate(t *testing.T) {
	install := newFakeInstall(t)
	stageRemotes(t, "r", map[string]*cliconfig.Remote{"r": {Master: install.srv.URL}})

	var out strings.Builder
	_, err := resolveForTest(t, &out, "\n", testFlowProject(t), &deployOptions{}, false, true)
	require.ErrorContains(t, err, "aborted")
	require.Empty(t, install.posts)
}

func TestSelectValuesDefaultsToStoredValues(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ".env", "A=1\n")
	project := &localProject{Root: root}

	// Under go test stdin is not a terminal, so this is the
	// non-interactive path: no flags means the stored values, no error.
	file, err := selectValues(io.Discard, project, &deployOptions{Environment: "production"})
	require.NoError(t, err)
	require.Nil(t, file)
}

func TestSelectValuesAutoUsesDotEnvOnly(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ".env.local", "A=1\n")
	project := &localProject{Root: root}

	file, err := selectValues(io.Discard, project, &deployOptions{Environment: "local", AutoEnvFile: true})
	require.NoError(t, err)
	require.Nil(t, file)

	dotenv := writeFile(t, root, ".env", "A=1\n")
	file, err = selectValues(io.Discard, project, &deployOptions{Environment: "local", AutoEnvFile: true})
	require.NoError(t, err)
	require.NotNil(t, file)
	require.Equal(t, dotenv, file.Path)
}

func TestPrintPlanShape(t *testing.T) {
	var out strings.Builder
	printPlan(&out, &client.PlanDocument{
		Project: "flowdemo",
		Changes: []client.PlanChange{
			{Service: "applications.web", Action: "update", Detail: "configuration changed"},
			{Service: "databases.data", Action: "remove", Destructive: true,
				Detail: "deletes the logical database and its data"},
		},
		Values: []client.PlanValueChange{
			{Name: "OLD_SECRET", Action: "prune"},
			{Name: "SESSION_SECRET", Action: "update"},
		},
	}, []client.ArtifactAction{{Application: "web", Action: "build"}}, "8d1e15b3aaaa")

	rendered := out.String()
	require.Contains(t, rendered, "plan against active revision 8d1e15b3aaaa")
	require.Contains(t, rendered, "artifact will be rebuilt")
	require.Contains(t, rendered, "DESTRUCTIVE: deletes the logical database")
	require.Contains(t, rendered, "SESSION_SECRET")
	require.Contains(t, rendered, "value   OLD_SECRET               prune stored value")
	require.NotContains(t, rendered, "no destructive changes")

	// Each reason renders on its own line: the change row carries the first
	// reason, later ones continue aligned under the detail column.
	lines := strings.Split(rendered, "\n")
	web := -1
	for i, line := range lines {
		if strings.Contains(line, "applications.web") {
			web = i
		}
	}
	require.NotEqual(t, -1, web)
	require.Contains(t, lines[web], "configuration changed")
	require.NotContains(t, lines[web], "artifact will be rebuilt")
	require.Contains(t, lines[web+1], "artifact will be rebuilt")
	require.NotContains(t, lines[web+1], "applications.web")
}

func TestValuesStagingFollowsAccess(t *testing.T) {
	// maintain and above stage; an unreported role (older fakes) passes.
	for _, role := range []string{"maintain", "admin", ""} {
		stage, err := valuesStagingAllowed(role, "production", &deployOptions{EnvFile: ".env", PruneValues: true})
		require.NoError(t, err, role)
		require.True(t, stage, role)
	}
	// deploy: a discovered file is skipped, an explicit one refused.
	stage, err := valuesStagingAllowed("deploy", "production", &deployOptions{})
	require.NoError(t, err)
	require.False(t, stage)
	_, err = valuesStagingAllowed("deploy", "production", &deployOptions{EnvFile: ".env"})
	require.ErrorContains(t, err, "staging values needs maintain on environment production (your role: deploy)")
	_, err = valuesStagingAllowed("deploy", "production", &deployOptions{PruneValues: true})
	require.ErrorContains(t, err, "pruning values needs maintain on environment production (your role: deploy)")

	require.NoError(t, checkDeployAccess("deploy", "production"))
	require.NoError(t, checkDeployAccess("", "production"))
	require.ErrorContains(t, checkDeployAccess("read", "production"), "deploy on environment production required (your role: read)")
	require.ErrorContains(t, checkDeployAccess("none", "production"), "deploy on environment production required (your role: none)")
}

// The client-side policy check mirrors the server: promote-only refuses a
// direct deploy and a promotion from an unlisted source unless the bypass
// is asked for by an environment admin; everything else passes untouched.
func TestCheckDeployPolicy(t *testing.T) {
	direct := &client.EnvironmentSettings{DeployPolicy: "direct", PromoteFrom: []string{"feat-x"}}
	protected := &client.EnvironmentSettings{DeployPolicy: "promote-only", PromoteFrom: []string{"staging", "qa"}}
	anySource := &client.EnvironmentSettings{DeployPolicy: "promote-only"}
	plain, bypass := &deployOptions{}, &deployOptions{BypassProtection: true}

	// Unprotected, unknown, and allowed promotions: no bypass consumed.
	for _, tc := range []struct {
		settings *client.EnvironmentSettings
		source   string
		opts     *deployOptions
	}{
		{nil, "", bypass}, {direct, "", plain}, {direct, "staging", bypass},
		{protected, "staging", plain}, {protected, "qa", bypass}, {anySource, "feat-x", plain},
	} {
		consumed, err := checkDeployPolicy(tc.settings, "read", "production", tc.source, tc.opts)
		require.NoError(t, err)
		require.False(t, consumed)
	}

	_, err := checkDeployPolicy(protected, "admin", "production", "", plain)
	require.EqualError(t, err, "environment production is promote-only: promote with skali deploy --from staging or qa "+
		"--environment production; environment admins may pass --bypass-protection")
	_, err = checkDeployPolicy(anySource, "admin", "production", "", plain)
	require.ErrorContains(t, err, "skali deploy --from <environment> --environment production")
	_, err = checkDeployPolicy(protected, "admin", "production", "feat-x", plain)
	require.EqualError(t, err, "environment production accepts promotions from staging or qa only, not from feat-x; "+
		"environment admins may pass --bypass-protection")
	_, err = checkDeployPolicy(protected, "maintain", "production", "", bypass)
	require.EqualError(t, err, "bypassing protection needs admin on environment production (your role: maintain)")
	for _, role := range []string{"admin", ""} {
		consumed, err := checkDeployPolicy(protected, role, "production", "", bypass)
		require.NoError(t, err, role)
		require.True(t, consumed, role)
	}
	consumed, err := checkDeployPolicy(protected, "admin", "production", "feat-x", bypass)
	require.NoError(t, err)
	require.True(t, consumed)
}
