package main

import (
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

func TestDiscoverEnvFilePreference(t *testing.T) {
	root := t.TempDir()
	require.Empty(t, discoverEnvFile(root, "production"))
	generic := writeFile(t, root, ".env", "A=1\n")
	require.Equal(t, generic, discoverEnvFile(root, "production"))
	scoped := writeFile(t, root, "production.env", "A=1\n")
	require.Equal(t, scoped, discoverEnvFile(root, "production"))
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
