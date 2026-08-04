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
	require.Equal(t, "build", hello.Definition.Applications["web"].Source.Kind)
	require.Empty(t, hello.Definition.Dependencies["applications.web"])
	require.Equal(t, []VariableRequirement{{Name: "APP_DOMAIN", Required: true, Runtime: true}}, hello.Definition.RequiredVariables)

	whoami := compileFixture(t, filepath.Join("..", "..", "examples", "whoami", "skali.yml"))
	require.Len(t, whoami.Definition.Applications, 1)
	require.Equal(t, "image", whoami.Definition.Applications["whoami"].Source.Kind)

	files := compileFixture(t, filepath.Join("..", "..", "examples", "file-sharing", "skali.yml"))
	require.Equal(t, []string{"buckets.files", "databases.data"}, files.Definition.Dependencies["applications.web"])
	variables := make(map[string]VariableRequirement, len(files.Definition.RequiredVariables))
	for _, requirement := range files.Definition.RequiredVariables {
		variables[requirement.Name] = requirement
	}
	require.True(t, variables["SESSION_SECRET"].Required)
	require.True(t, variables["SESSION_SECRET"].Runtime)
	require.False(t, variables["SESSION_SECRET"].Build)
	require.True(t, variables["APP_DOMAIN"].Runtime)
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
	require.Equal(t, "/", result.Definition.Applications["web"].Routes["public"].Path.Literal())
	require.Equal(t, "/api", result.Definition.Applications["api"].Routes["public"].Path.Literal())
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

// The variable contract is derived entirely from references: scope flags
// follow the position, and one name may span both scopes.
func TestVariableScopesFollowReferences(t *testing.T) {
	t.Parallel()
	result, err := compileManifest(t, `
version: "1"
name: scoped-values
applications:
  api:
    build:
      context: .
      target: "${BUILD_TARGET:-runtime}"
      arguments:
        NPM_TOKEN: "${NPM_TOKEN}"
    ports:
      http:
        port: 8080
    routes:
      public:
        domain: "${APP_DOMAIN}"
        port: http
    environment:
      NPM_TOKEN: "${NPM_TOKEN}"
      API_KEY: "${API_KEY:-fallback}"
`)
	require.NoError(t, err)
	variables := make(map[string]VariableRequirement, len(result.Definition.RequiredVariables))
	for _, requirement := range result.Definition.RequiredVariables {
		variables[requirement.Name] = requirement
	}
	require.Equal(t, VariableRequirement{Name: "NPM_TOKEN", Required: true, Runtime: true, Build: true}, variables["NPM_TOKEN"])
	require.Equal(t, VariableRequirement{Name: "APP_DOMAIN", Required: true, Runtime: true}, variables["APP_DOMAIN"])
	require.Equal(t, VariableRequirement{Name: "BUILD_TARGET", Default: "runtime", HasDefault: true, Build: true}, variables["BUILD_TARGET"])
	require.Equal(t, VariableRequirement{Name: "API_KEY", Default: "fallback", HasDefault: true, Runtime: true}, variables["API_KEY"])
}

// ${NAME} references concatenate with literals anywhere, including
// environment values; only {{...}} service outputs must stand alone.
func TestEnvironmentValueConcatenation(t *testing.T) {
	t.Parallel()
	result, err := compileManifest(t, `
version: "1"
name: concatenation
applications:
  api:
    image: example.invalid/api:1
    command: ["serve", "--host", "${APP_DOMAIN}"]
    environment:
      DATABASE_URL: "postgres://app:${DB_PASSWORD}@db:5432/app"
`)
	require.NoError(t, err)
	expression := result.Definition.Applications["api"].Environment["DATABASE_URL"]
	require.Len(t, expression.Parts, 3)
	require.True(t, expression.HasProjectVariables())
	variables := make(map[string]VariableRequirement, len(result.Definition.RequiredVariables))
	for _, requirement := range result.Definition.RequiredVariables {
		variables[requirement.Name] = requirement
	}
	require.True(t, variables["DB_PASSWORD"].Runtime)
	require.True(t, variables["APP_DOMAIN"].Runtime)
}

func TestServiceOutputMustOccupyEntireEnvironmentValue(t *testing.T) {
	t.Parallel()
	_, err := compileManifest(t, `
version: "1"
name: output-concat
applications:
  api:
    image: example.invalid/api:1
    environment:
      DATABASE_URL: "{{databases.data.url}}?sslmode=require"
databases:
  data:
    engine: postgres
    version: 17
`)
	require.ErrorContains(t, err, "a service output must occupy the entire environment value")
}

// The values: block no longer exists; the strict parser rejects manifests
// that still carry one.
func TestValuesBlockIsRejected(t *testing.T) {
	t.Parallel()
	_, err := manifest.Parse([]byte(`
version: "1"
name: legacy-values
values:
  APP_DOMAIN:
    description: legacy declaration
applications:
  api:
    image: example.invalid/api:1
`), "skali.yml")
	require.ErrorContains(t, err, "values")
}

func TestValueNamesMustBeEnvironmentStyle(t *testing.T) {
	t.Parallel()
	_, err := compileManifest(t, `
version: "1"
name: bad-value-name
applications:
  api:
    image: example.invalid/api:1
    environment:
      X: "${lowercase}"
`)
	require.ErrorContains(t, err, "malformed variable or service-output expression")
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
