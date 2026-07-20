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
	variables := make(map[string]VariableRequirement, len(files.Definition.RequiredVariables))
	for _, requirement := range files.Definition.RequiredVariables {
		variables[requirement.Name] = requirement
	}
	require.True(t, variables["SESSION_SECRET"].Secret)
	require.NotEmpty(t, variables["SESSION_SECRET"].Description)
	require.False(t, variables["APP_DOMAIN"].Secret)
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

func TestSecretValueRejectedOutsideApplicationEnvironment(t *testing.T) {
	t.Parallel()
	_, err := compileManifest(t, `
version: "1"
name: secret-route
values:
  APP_DOMAIN:
    secret: true
applications:
  api:
    image: example.invalid/api:1
    ports:
      http:
        port: 8080
    routes:
      public:
        domain: "${APP_DOMAIN}"
        port: http
`)
	require.ErrorContains(t, err, "secret project value APP_DOMAIN may only be used in application environment variables")
}

// Build arguments persist in image history, so a secret value can never
// legally reach one; the build engine's secret mounts stay the only path
// for secret build inputs.
func TestSecretValueRejectedInBuildArguments(t *testing.T) {
	t.Parallel()
	_, err := compileManifest(t, `
version: "1"
name: secret-build-arg
values:
  NPM_TOKEN:
    secret: true
applications:
  api:
    build:
      context: .
      arguments:
        NPM_TOKEN: "${NPM_TOKEN}"
    ports:
      http:
        port: 8080
    environment:
      NPM_TOKEN: "${NPM_TOKEN}"
`)
	require.ErrorContains(t, err, "secret project value NPM_TOKEN may only be used in application environment variables")
}

func TestSecretValueRejectsInlineDefault(t *testing.T) {
	t.Parallel()
	_, err := compileManifest(t, `
version: "1"
name: secret-default
values:
  API_KEY:
    secret: true
applications:
  api:
    image: example.invalid/api:1
    environment:
      API_KEY: "${API_KEY:-fallback}"
`)
	require.ErrorContains(t, err, "secret project value API_KEY cannot carry an inline default")
}

func TestDeclaredValueMustBeReferenced(t *testing.T) {
	t.Parallel()
	_, err := compileManifest(t, `
version: "1"
name: unused-value
values:
  UNUSED:
    description: never referenced
applications:
  api:
    image: example.invalid/api:1
`)
	require.ErrorContains(t, err, "values.UNUSED: is declared but never referenced")
}

func TestValueNamesMustBeEnvironmentStyle(t *testing.T) {
	t.Parallel()
	_, err := compileManifest(t, `
version: "1"
name: bad-value-name
values:
  lowercase:
    description: wrong shape
applications:
  api:
    image: example.invalid/api:1
    environment:
      X: "${lowercase}"
`)
	require.ErrorContains(t, err, "value names must be uppercase environment-variable names")
}

func TestRollingUpdateDefaultsSurgeWhenUnavailableIsSpecified(t *testing.T) {
	t.Parallel()
	result, err := compileManifest(t, `
version: "1"
name: rollout-defaults
applications:
  api:
    image: example.invalid/api:1
    deployment:
      rollout:
        maxUnavailable: 0
`)
	require.NoError(t, err)
	require.Equal(t, Rollout{
		Strategy:       "rolling",
		MaxUnavailable: 0,
		MaxSurge:       1,
	}, result.Definition.Applications["api"].Deployment.Rollout)
}

func TestRollingUpdateRejectsZeroUnavailableAndSurge(t *testing.T) {
	t.Parallel()
	_, err := compileManifest(t, `
version: "1"
name: invalid-rollout
applications:
  api:
    image: example.invalid/api:1
    deployment:
      rollout:
        maxUnavailable: 0
        maxSurge: 0
`)
	require.ErrorContains(t, err, "maxUnavailable and maxSurge cannot both be zero")
}

func TestRollingUpdateRejectsNegativeValues(t *testing.T) {
	t.Parallel()
	_, err := compileManifest(t, `
version: "1"
name: invalid-rollout
applications:
  api:
    image: example.invalid/api:1
    deployment:
      rollout:
        maxUnavailable: -1
`)
	require.ErrorContains(t, err, "maxUnavailable: must not be negative")
}

func TestVolumeBackedApplicationDefaultsToRecreate(t *testing.T) {
	t.Parallel()
	result, err := compileManifest(t, `
version: "1"
name: volume-rollout
applications:
  api:
    image: example.invalid/api:1
    volumes:
      data:
        mountPath: /data
        size: 1GB
`)
	require.NoError(t, err)
	require.Equal(t, "recreate", result.Definition.Applications["api"].Deployment.Rollout.Strategy)
}

func TestVolumeBackedApplicationRejectsRollingStrategy(t *testing.T) {
	t.Parallel()
	_, err := compileManifest(t, `
version: "1"
name: invalid-volume-rollout
applications:
  api:
    image: example.invalid/api:1
    deployment:
      rollout:
        strategy: rolling
    volumes:
      data:
        mountPath: /data
        size: 1GB
`)
	require.ErrorContains(t, err, "persistent volumes currently require recreate rollout strategy")
}

func TestRecreateRejectsRollingUpdateControls(t *testing.T) {
	t.Parallel()
	_, err := compileManifest(t, `
version: "1"
name: invalid-recreate
applications:
  api:
    image: example.invalid/api:1
    deployment:
      rollout:
        strategy: recreate
        maxSurge: 1
`)
	require.ErrorContains(t, err, "maxSurge: is only valid when strategy is rolling")
}

func compileManifest(t *testing.T, source string) (*Result, error) {
	t.Helper()
	document, err := manifest.Parse([]byte(source), "skali.yml")
	require.NoError(t, err)
	return Compile(document)
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
