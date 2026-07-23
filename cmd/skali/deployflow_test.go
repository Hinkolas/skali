package main

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

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
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/projects", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"projects":[{"id":"p1","name":"file-sharing"}]}`))
	})
	mux.HandleFunc("/v1/projects/p1/environments", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"environments":[{"id":"e1","name":"production"},{"id":"e2","name":"staging"}]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	api := client.New(srv.URL, "tok", "")

	var out strings.Builder
	name, err := chooseEnvironment(context.Background(), &out, bufio.NewReader(strings.NewReader("2\n")), api, "file-sharing")
	require.NoError(t, err)
	require.Equal(t, "staging", name)
	require.Contains(t, out.String(), "Environment:\n")
	require.Contains(t, out.String(), "  2) staging\n")

	_, err = chooseEnvironment(context.Background(), &out, bufio.NewReader(strings.NewReader("")), api, "unknown")
	require.ErrorContains(t, err, "project unknown does not exist")
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
