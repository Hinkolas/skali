package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/checkout"
	"github.com/Hinkolas/skali/internal/cliconfig"
	"github.com/Hinkolas/skali/internal/client"
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
values:
  APP_DOMAIN: {}
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

	inputs, contexts, err := buildInputs(project, map[string]string{"APP_DOMAIN": "flow.localhost"}, []string{envFile})
	require.NoError(t, err)
	require.Contains(t, inputs, "web")
	require.Len(t, inputs["web"].InputHash, 64)
	require.True(t, strings.HasPrefix(inputs["web"].Platform, "linux/"))
	// The env file never enters the context inventory.
	require.NotContains(t, contexts["web"].Files, ".env")

	// A source change moves the input hash; the dedup key is honest.
	writeFile(t, root, "main.txt", "changed")
	changed, _, err := buildInputs(project, map[string]string{"APP_DOMAIN": "flow.localhost"}, []string{envFile})
	require.NoError(t, err)
	require.NotEqual(t, inputs["web"].InputHash, changed["web"].InputHash)
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

func TestPromptSelect(t *testing.T) {
	var out strings.Builder
	in := bufio.NewReader(strings.NewReader("nope\n2\n"))
	choice, err := promptSelect(&out, in, []string{"production", "staging"}, 1, -1)
	require.NoError(t, err)
	require.Equal(t, 1, choice)
	require.Contains(t, out.String(), "  1) production\n")
	require.Contains(t, out.String(), "Select [1-2]: ")

	out.Reset()
	in = bufio.NewReader(strings.NewReader("\n"))
	choice, err = promptSelect(&out, in, []string{"no, use the stored values", ".env"}, 0, 0)
	require.NoError(t, err)
	require.Equal(t, 0, choice)
	require.Contains(t, out.String(), "Select [0-1] (0): ")

	// EOF without a valid answer is an error, not an infinite loop.
	in = bufio.NewReader(strings.NewReader("9\n"))
	_, err = promptSelect(&out, in, []string{"a", "b"}, 1, -1)
	require.Error(t, err)
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

	selected, err = chooseEnvFile(&out, bufio.NewReader(strings.NewReader("2\n")), root, "production")
	require.NoError(t, err)
	require.Equal(t, production, selected)
	require.Contains(t, out.String(), "Override the stored values of environment production")
	require.Contains(t, out.String(), "  0) no, use the stored values\n")

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
	require.Contains(t, out.String(), "Environment:\n")
	require.Contains(t, out.String(), "  2) staging\n")

	// A single environment selects itself without a prompt.
	out.Reset()
	name, err = chooseEnvironment(&out, bufio.NewReader(strings.NewReader("")), environments[:1])
	require.NoError(t, err)
	require.Equal(t, "production", name)
	require.Empty(t, out.String())
}

func TestPromptText(t *testing.T) {
	var out strings.Builder
	answer := promptText(&out, bufio.NewReader(strings.NewReader("staging\n")), "Environment name", "production")
	require.Equal(t, "staging", answer)
	require.Equal(t, "Environment name (production): ", out.String())

	answer = promptText(&out, bufio.NewReader(strings.NewReader("\n")), "Environment name", "production")
	require.Equal(t, "production", answer)
}

func TestConfirmLine(t *testing.T) {
	var out strings.Builder
	require.True(t, confirmLine(&out, bufio.NewReader(strings.NewReader("y\n")), "Create? [y/N] "))
	require.Equal(t, "Create? [y/N] ", out.String())
	require.True(t, confirmLine(&out, bufio.NewReader(strings.NewReader("YES\n")), "Create? [y/N] "))
	require.False(t, confirmLine(&out, bufio.NewReader(strings.NewReader("\n")), "Create? [y/N] "))
	require.False(t, confirmLine(&out, bufio.NewReader(strings.NewReader("")), "Create? [y/N] "))
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

// fakeInstall is a minimal stateful projects/environments API for target
// resolution tests, recording creations.
type fakeInstall struct {
	mu       sync.Mutex
	projects []client.Project
	envs     map[string][]client.Environment
	posts    []string
	srv      *httptest.Server
}

func newFakeInstall(t *testing.T) *fakeInstall {
	t.Helper()
	f := &fakeInstall{envs: map[string][]client.Environment{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/projects", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Method == http.MethodPost {
			var req struct {
				Name string `json:"name"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			created := client.Project{ID: fmt.Sprintf("p%d", len(f.projects)+1), Name: req.Name}
			f.projects = append(f.projects, created)
			f.posts = append(f.posts, "project:"+req.Name)
			_ = json.NewEncoder(w).Encode(map[string]any{"project": created})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"projects": f.projects})
	})
	mux.HandleFunc("/v1/projects/", func(w http.ResponseWriter, r *http.Request) {
		projectID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/projects/"), "/environments")
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Method == http.MethodPost {
			var req struct {
				Name string `json:"name"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			created := client.Environment{ID: fmt.Sprintf("e%d", len(f.envs[projectID])+1),
				ProjectID: projectID, Name: req.Name}
			f.envs[projectID] = append(f.envs[projectID], created)
			f.posts = append(f.posts, "environment:"+projectID+":"+req.Name)
			_ = json.NewEncoder(w).Encode(map[string]any{"environment": created})
			return
		}
		envs := f.envs[projectID]
		if envs == nil {
			envs = []client.Environment{}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"environments": envs})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// seed installs a project with the given environments.
func (f *fakeInstall) seed(projectID, projectName string, envNames ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.projects = append(f.projects, client.Project{ID: projectID, Name: projectName})
	for i, name := range envNames {
		f.envs[projectID] = append(f.envs[projectID],
			client.Environment{ID: fmt.Sprintf("%s-e%d", projectID, i+1), ProjectID: projectID, Name: name})
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

	// The dev-owned local remote is never bound.
	stageRemotes(t, "local", map[string]*cliconfig.Remote{"local": {Master: install.srv.URL}})
	local := testFlowProject(t)
	out.Reset()
	_, err = resolveForTest(t, &out, "", local,
		&deployOptions{UseBinding: true, Environment: "production"}, true, false)
	require.NoError(t, err)
	require.NotContains(t, out.String(), "linked to remote")
	binding, err = checkout.Load(local.Root)
	require.NoError(t, err)
	require.Nil(t, binding)
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
	require.Contains(t, out.String(), "Environment name (production): ")
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
		Values: []client.PlanValueChange{{Name: "SESSION_SECRET", Action: "update", Secret: true}},
	}, []client.ArtifactAction{{Application: "web", Action: "build"}}, "8d1e15b3aaaa")

	rendered := out.String()
	require.Contains(t, rendered, "plan against active revision 8d1e15b3aaaa")
	require.Contains(t, rendered, "artifact will be rebuilt")
	require.Contains(t, rendered, "DESTRUCTIVE: deletes the logical database")
	require.Contains(t, rendered, "SESSION_SECRET")
	require.NotContains(t, rendered, "no destructive changes")
}
