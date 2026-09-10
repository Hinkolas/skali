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
	require.Equal(t, []VariableRequirement{{Name: "APP_DOMAIN", Required: true}}, hello.Definition.RequiredVariables)

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
	require.Contains(t, variables, "APP_DOMAIN")
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

// The dockerfile value resolves against the build context (docker
// convention); the compiled definition carries the project-root-relative
// join. It may leave the context via "..", but never the project root.
func TestDockerfileResolvesAgainstContext(t *testing.T) {
	t.Parallel()
	compile := func(t *testing.T, context, dockerfile string) (Build, error) {
		t.Helper()
		source := "version: \"1\"\nname: dockerfiles\napplications:\n  api:\n    build:\n      context: " + context + "\n"
		if dockerfile != "" {
			source += "      dockerfile: " + dockerfile + "\n"
		}
		document, err := manifest.Parse([]byte(source), "skali.yml")
		require.NoError(t, err)
		result, err := Compile(document)
		if err != nil {
			return Build{}, err
		}
		return result.Definition.Applications["api"].Source.Build, nil
	}

	resolved := map[string]struct{ context, dockerfile, want string }{
		"root context default":    {".", "", "Dockerfile"},
		"nested context default":  {"./web", "", "web/Dockerfile"},
		"nested context explicit": {"./web", "deploy/Dockerfile", "web/deploy/Dockerfile"},
		"outside context":         {"./services/worker", "../shared.Dockerfile", "services/shared.Dockerfile"},
	}
	for name, test := range resolved {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			build, err := compile(t, test.context, test.dockerfile)
			require.NoError(t, err)
			require.Equal(t, test.want, build.Dockerfile)
		})
	}

	invalid := map[string]struct{ context, dockerfile, message string }{
		"escapes root":  {".", "../Dockerfile", "must not escape the project root"},
		"absolute path": {".", "/etc/Dockerfile", "must be relative to the build context"},
		"names no file": {"./web", ".", "must name a file relative to the build context"},
	}
	for name, test := range invalid {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := compile(t, test.context, test.dockerfile)
			require.ErrorContains(t, err, test.message)
		})
	}
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

func TestRoutePolicyDefaultsAndBounds(t *testing.T) {
	t.Parallel()
	result, err := compileManifest(t, `
version: "1"
name: route-policies
applications:
  api:
    image: example.invalid/api:1
    ports:
      http:
        port: 8080
    routes:
      public:
        domain: api.example.com
        port: http
      admin:
        domain: admin.example.com
        port: http
        tls: optional
        strategy: least-requests
`)
	require.NoError(t, err)
	routes := result.Definition.Applications["api"].Routes
	require.Equal(t, "automatic", routes["public"].TLS)
	require.Equal(t, "round-robin", routes["public"].Strategy)
	require.Equal(t, "optional", routes["admin"].TLS)
	require.Equal(t, "least-requests", routes["admin"].Strategy)

	_, err = compileManifest(t, `
version: "1"
name: route-policies
applications:
  api:
    image: example.invalid/api:1
    ports:
      http:
        port: 8080
    routes:
      public:
        domain: api.example.com
        port: http
        strategy: fastest
`)
	require.ErrorContains(t, err, "must be round-robin or least-requests")
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

// Interpolation is limited to environment values and route domains: build
// fields carry ${...} text through verbatim and register no requirement.
func TestBuildFieldsAreLiteral(t *testing.T) {
	t.Parallel()
	result, err := compileManifest(t, `
version: "1"
name: literal-build
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
      API_KEY: "${API_KEY:-fallback}"
`)
	require.NoError(t, err)
	build := result.Definition.Applications["api"].Source.Build
	require.Equal(t, "${BUILD_TARGET:-runtime}", build.Target)
	require.Equal(t, map[string]string{"NPM_TOKEN": "${NPM_TOKEN}"}, build.Arguments)
	variables := make(map[string]VariableRequirement, len(result.Definition.RequiredVariables))
	for _, requirement := range result.Definition.RequiredVariables {
		variables[requirement.Name] = requirement
	}
	require.NotContains(t, variables, "BUILD_TARGET")
	require.NotContains(t, variables, "NPM_TOKEN")
	require.Equal(t, VariableRequirement{Name: "APP_DOMAIN", Required: true}, variables["APP_DOMAIN"])
	require.Equal(t, VariableRequirement{Name: "API_KEY", Default: "fallback", HasDefault: true}, variables["API_KEY"])
}

func TestPlatformsCompileCanonically(t *testing.T) {
	t.Parallel()
	result, err := compileManifest(t, `
version: "1"
name: platform-demo
applications:
  api:
    build:
      context: .
    platforms: [linux/arm64, linux/amd64]
  web:
    image: example.invalid/web:1
    platforms: [linux/amd64]
  plain:
    image: example.invalid/plain:1
`)
	require.NoError(t, err)
	require.Equal(t, []string{"linux/amd64", "linux/arm64"}, result.Definition.Applications["api"].Source.Platforms)
	require.Equal(t, []string{"linux/amd64"}, result.Definition.Applications["web"].Source.Platforms)
	require.Nil(t, result.Definition.Applications["plain"].Source.Platforms)
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
	require.Contains(t, variables, "DB_PASSWORD")
	// Command elements are plain strings now; a ${...} there is shell text,
	// not a project variable reference.
	require.Equal(t, []string{"serve", "--host", "${APP_DOMAIN}"}, result.Definition.Applications["api"].Command)
	require.NotContains(t, variables, "APP_DOMAIN")
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
	require.ErrorContains(t, err, "project value names match ^[A-Z_][A-Z0-9_]*$")
}

func TestRolloutDefaultsToBlueGreen(t *testing.T) {
	t.Parallel()
	result, err := compileManifest(t, `
version: "1"
name: rollout-default
applications:
  api:
    image: example.invalid/api:1
    scaling:
      replicas:
        min: 2
        max: 5
      autoscaling:
        cpu:
          targetUtilization: 70
`)
	require.NoError(t, err)
	require.Equal(t, Rollout{Strategy: StrategyBlueGreen}, result.Definition.Applications["api"].Deployment.Rollout,
		"no rolling-update controls and no timeout default leak into the blue-green IR")
}

func TestRolloutRejectsUnknownStrategy(t *testing.T) {
	t.Parallel()
	_, err := compileManifest(t, `
version: "1"
name: invalid-rollout
applications:
  api:
    image: example.invalid/api:1
    deployment:
      rollout:
        strategy: canary
`)
	require.ErrorContains(t, err, "strategy: must be blue-green, rolling, or recreate")
}

func TestBlueGreenRejectsRollingUpdateControls(t *testing.T) {
	t.Parallel()
	_, err := compileManifest(t, `
version: "1"
name: invalid-blue-green
applications:
  api:
    image: example.invalid/api:1
    deployment:
      rollout:
        strategy: blue-green
        maxUnavailable: 0
`)
	require.ErrorContains(t, err, "maxUnavailable: is only valid when strategy is rolling")
}

func TestVolumeBackedApplicationRejectsBlueGreenStrategy(t *testing.T) {
	t.Parallel()
	_, err := compileManifest(t, `
version: "1"
name: invalid-volume-rollout
applications:
  api:
    image: example.invalid/api:1
    deployment:
      rollout:
        strategy: blue-green
    volumes:
      data:
        mountPath: /data
        size: 1GB
`)
	require.ErrorContains(t, err, "persistent volumes currently require recreate rollout strategy")
}

func TestRolloutTimeoutIsCompiledForEveryStrategy(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		StrategyBlueGreen: "",
		StrategyRolling:   "",
		StrategyRecreate:  "    volumes:\n      data:\n        mountPath: /data\n        size: 1GB\n",
	}
	for strategy, extra := range cases {
		result, err := compileManifest(t, `
version: "1"
name: rollout-timeout
applications:
  api:
    image: example.invalid/api:1
    deployment:
      rollout:
        strategy: `+strategy+`
        timeout: 15m
`+extra)
		require.NoError(t, err, strategy)
		rollout := result.Definition.Applications["api"].Deployment.Rollout
		require.Equal(t, strategy, rollout.Strategy)
		require.Equal(t, int64(15*60*1000), rollout.TimeoutMillis, strategy)
	}
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
        strategy: rolling
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
        strategy: rolling
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
        strategy: rolling
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
