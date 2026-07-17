package compiler

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/manifest"
)

func TestCompileExamples(t *testing.T) {
	t.Parallel()

	hello := compileFixture(t, filepath.Join("..", "..", "examples", "hello-world", "skali.yml"))
	require.Len(t, hello.Definition.Applications, 1)
	require.Empty(t, hello.Definition.Dependencies["applications.api"])
	require.Equal(t, []VariableRequirement{{Name: "APP_DOMAIN", Required: true}}, hello.Definition.RequiredVariables)

	files := compileFixture(t, filepath.Join("..", "..", "examples", "file-sharing", "skali.yml"))
	require.Equal(t, []string{"buckets.files", "databases.data"}, files.Definition.Dependencies["applications.web"])
	require.EqualValues(t, 200, files.Definition.Applications["web"].Resources.Requests.MilliCPU)
	require.EqualValues(t, 256_000_000, files.Definition.Applications["web"].Resources.Requests.MemoryBytes)
	require.EqualValues(t, 20_000_000_000, files.Definition.Databases["data"].StorageBytes)
	require.True(t, files.Definition.Backups["daily"].Include.AllDatabases)
	require.True(t, files.Definition.Backups["daily"].Include.AllBuckets)
}

func TestEquivalentYAMLAndJSONHaveSameHash(t *testing.T) {
	t.Parallel()
	yamlDocument, err := manifest.Parse([]byte("version: \"1\"\nname: equivalent\napplications:\n  api:\n    image: example.invalid/api:1\n    resources:\n      requests:\n        cpu: 0.2\n"), "skali.yml")
	require.NoError(t, err)
	jsonDocument, err := manifest.Parse([]byte(`{"version":"1","name":"equivalent","applications":{"api":{"image":"example.invalid/api:1","resources":{"requests":{"cpu":"0.2"}}}}}`), "skali.json")
	require.NoError(t, err)

	yamlResult, err := Compile(yamlDocument)
	require.NoError(t, err)
	jsonResult, err := Compile(jsonDocument)
	require.NoError(t, err)
	require.Equal(t, yamlResult.Hash, jsonResult.Hash)
	require.Equal(t, yamlResult.Definition, jsonResult.Definition)
}

func TestInvalidFixtures(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"image-and-build.yml": "exactly one of image or build",
		"invalid-output.yml":  "unknown database output \"hostname\"",
		"invalid-unit.yml":    "Skali byte unit",
		"route-conflict.yml":  "domain and path pairs must be unique",
	}
	for name, message := range tests {
		name, message := name, message
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			document, err := manifest.ParseFile(filepath.Join("testdata", name))
			require.NoError(t, err)
			_, err = Compile(document)
			require.ErrorContains(t, err, message)
		})
	}
}

func TestSameDomainSupportsDistinctRoutePaths(t *testing.T) {
	t.Parallel()
	result := compileFixture(t, filepath.Join("testdata", "route-paths.yml"))
	require.Equal(t, "/", result.Definition.Applications["web"].Routes["public"].Path)
	require.Equal(t, "/api", result.Definition.Applications["api"].Routes["public"].Path)
}

func TestProjectVariableDefault(t *testing.T) {
	t.Parallel()
	expression, err := parseExpression("${POSTGRES_USER:-app}", manifest.Project{}, false)
	require.NoError(t, err)
	value, err := ResolveExpression(expression, nil)
	require.NoError(t, err)
	require.Equal(t, "app", value)
	value, err = ResolveExpression(expression, map[string]string{"POSTGRES_USER": "custom"})
	require.NoError(t, err)
	require.Equal(t, "custom", value)
}

func TestValidateEnvironment(t *testing.T) {
	t.Parallel()
	result := compileFixture(t, filepath.Join("..", "..", "examples", "hello-world", "skali.yml"))
	require.ErrorContains(t, ValidateEnvironment(result, nil), "APP_DOMAIN")
	require.NoError(t, ValidateEnvironment(result, map[string]string{"APP_DOMAIN": "hello.localhost"}))
	require.ErrorContains(t, ValidateEnvironment(result, map[string]string{
		"APP_DOMAIN": "hello.localhost",
		"UNUSED":     "value",
	}), "unknown project variables: UNUSED")
}

func compileFixture(t *testing.T, path string) *Result {
	t.Helper()
	document, err := manifest.ParseFile(path)
	require.NoError(t, err)
	result, err := Compile(document)
	require.NoError(t, err)
	require.Len(t, result.Hash, 64)
	return result
}
