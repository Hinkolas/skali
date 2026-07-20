package revision

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/manifest"
	"github.com/Hinkolas/skali/internal/values"
)

// Two semantically identical manifests with every map in a different key
// order: applications, values, ports, environment.
const orderedManifest = `version: "1"
name: demo
values:
  API_KEY:
    secret: true
  APP_DOMAIN:
    description: public domain
applications:
  api:
    image: example.invalid/api:1
    ports:
      http:
        port: 8080
        protocol: http
      metrics:
        port: 9090
        protocol: tcp
    environment:
      API_KEY: "${API_KEY}"
      APP_DOMAIN: "${APP_DOMAIN}"
  worker:
    image: example.invalid/worker:1
`

const reorderedManifest = `version: "1"
name: demo
applications:
  worker:
    image: example.invalid/worker:1
  api:
    environment:
      APP_DOMAIN: "${APP_DOMAIN}"
      API_KEY: "${API_KEY}"
    ports:
      metrics:
        protocol: tcp
        port: 9090
      http:
        protocol: http
        port: 8080
    image: example.invalid/api:1
values:
  APP_DOMAIN:
    description: public domain
  API_KEY:
    secret: true
`

func compileSource(t *testing.T, source string) *compiler.Result {
	t.Helper()
	document, err := manifest.Parse([]byte(source), "skali.yml")
	require.NoError(t, err)
	result, err := compiler.Compile(document)
	require.NoError(t, err)
	return result
}

// Exit criterion: reordering maps does not change a revision. The manifest
// maps, the value maps, and the artifact maps are all built in different
// orders; the checksums must be identical.
func TestBuildMapOrderInvariance(t *testing.T) {
	t.Parallel()
	input := func(result *compiler.Result, reversed bool) Input {
		plain := map[string]string{}
		secret := map[string]string{}
		artifacts := map[string]Artifact{}
		if reversed {
			artifacts["worker"] = Artifact{Reference: "r/worker", Digest: digest("b"), Kind: KindImport}
			artifacts["api"] = Artifact{Reference: "r/api", Digest: digest("a"), Kind: KindImport}
			secret["API_KEY"] = "irrelevant-plaintext"
			plain["APP_DOMAIN"] = "demo.example.com"
		} else {
			plain["APP_DOMAIN"] = "demo.example.com"
			secret["API_KEY"] = "irrelevant-plaintext"
			artifacts["api"] = Artifact{Reference: "r/api", Digest: digest("a"), Kind: KindImport}
			artifacts["worker"] = Artifact{Reference: "r/worker", Digest: digest("b"), Kind: KindImport}
		}
		return Input{
			Result:          result,
			Environment:     "production",
			Values:          values.Resolved{Plain: plain, Secret: secret},
			Artifacts:       artifacts,
			CompilerVersion: "test",
		}
	}

	first, err := Build(input(compileSource(t, orderedManifest), false))
	require.NoError(t, err)
	second, err := Build(input(compileSource(t, reorderedManifest), true))
	require.NoError(t, err)
	require.Equal(t, first.DefinitionHash, second.DefinitionHash)
	require.Equal(t, first.ValuesHash, second.ValuesHash)
	require.Equal(t, first.Checksum, second.Checksum)
}

// Exit criterion: secrets cannot appear in revision fixtures. The golden
// documents are scanned for the planted secret plaintexts their inputs use.
func TestGoldenFixturesContainNoSecretPlaintext(t *testing.T) {
	t.Parallel()
	planted := []string{"test-only-secret"}
	entries, err := os.ReadDir("testdata")
	require.NoError(t, err)
	require.NotEmpty(t, entries)
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join("testdata", entry.Name()))
		require.NoError(t, err)
		for _, secret := range planted {
			require.NotContains(t, string(data), secret,
				"golden fixture %s contains secret plaintext", entry.Name())
		}
	}
}
