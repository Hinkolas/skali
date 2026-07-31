package revision

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/layout"
	"github.com/Hinkolas/skali/internal/manifest"
	"github.com/Hinkolas/skali/internal/values"
)

func digest(fill string) string {
	return "sha256:" + strings.Repeat(fill, 64)
}

func compileExample(t *testing.T, name string) *compiler.Result {
	t.Helper()
	document, err := manifest.ParseFile(filepath.Join("..", "..", "examples", name, "skali.yml"))
	require.NoError(t, err)
	result, err := compiler.Compile(document)
	require.NoError(t, err)
	return result
}

func helloWorldInput(t *testing.T) Input {
	return Input{
		Result:      compileExample(t, "hello-world"),
		Environment: "production",
		Values: values.Resolved{
			Plain:  map[string]string{"APP_DOMAIN": "hello.example.com"},
			Secret: map[string]string{},
		},
		Artifacts: map[string]Artifact{
			"web": {
				Reference:   "registry.skali.internal/skali/hello-world/web",
				Digest:      digest("1"),
				Kind:        KindBuildLocal,
				ContextHash: digest("4"),
			},
		},
		CompilerVersion: "test",
	}
}

func fileSharingInput(t *testing.T) Input {
	return Input{
		Result:      compileExample(t, "file-sharing"),
		Environment: "production",
		Values: values.Resolved{
			Plain:  map[string]string{"APP_DOMAIN": "files.example.com"},
			Secret: map[string]string{"SESSION_SECRET": "test-only-secret"},
		},
		Artifacts: map[string]Artifact{
			"web": {
				Reference:   "registry.skali.internal/skali/file-sharing/web",
				Digest:      digest("2"),
				Kind:        KindBuildLocal,
				ContextHash: digest("3"),
			},
		},
		CompilerVersion: "test",
	}
}

// TestGoldenRevisions pins the revision document format. Regenerate with
// UPDATE_GOLDEN=1 go test ./internal/revision.
func TestGoldenRevisions(t *testing.T) {
	t.Parallel()
	cases := map[string]Input{
		"hello-world":  helloWorldInput(t),
		"file-sharing": fileSharingInput(t),
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			built, err := Build(input)
			require.NoError(t, err)
			data, err := json.MarshalIndent(built, "", "  ")
			require.NoError(t, err)
			data = append(data, '\n')

			golden := filepath.Join("testdata", name+".golden.json")
			if os.Getenv("UPDATE_GOLDEN") != "" {
				require.NoError(t, os.WriteFile(golden, data, 0o644))
			}
			expected, err := os.ReadFile(golden)
			require.NoError(t, err)
			require.Equal(t, string(expected), string(data))
		})
	}
}

func TestChecksumIsDeterministicAndInputSensitive(t *testing.T) {
	t.Parallel()
	first, err := Build(fileSharingInput(t))
	require.NoError(t, err)
	second, err := Build(fileSharingInput(t))
	require.NoError(t, err)
	require.Equal(t, first.Checksum, second.Checksum)

	changed := fileSharingInput(t)
	changed.Values.Plain["APP_DOMAIN"] = "other.example.com"
	third, err := Build(changed)
	require.NoError(t, err)
	require.NotEqual(t, first.ValuesHash, third.ValuesHash)
	require.NotEqual(t, first.Checksum, third.Checksum)
}

func TestSecretPlaintextNeverEntersTheRevision(t *testing.T) {
	t.Parallel()
	built, err := Build(fileSharingInput(t))
	require.NoError(t, err)
	data, err := json.Marshal(built)
	require.NoError(t, err)
	require.NotContains(t, string(data), "test-only-secret")
	require.Equal(t, SecretRef{Version: 1}, built.Secrets["SESSION_SECRET"])
}

func TestSecretVersionsAreRecorded(t *testing.T) {
	t.Parallel()
	input := fileSharingInput(t)
	input.SecretVersions = map[string]int{"SESSION_SECRET": 4}
	built, err := Build(input)
	require.NoError(t, err)
	require.Equal(t, SecretRef{Version: 4}, built.Secrets["SESSION_SECRET"])
}

func TestCapabilityDerivation(t *testing.T) {
	t.Parallel()
	hello, err := Build(helloWorldInput(t))
	require.NoError(t, err)
	require.Equal(t, []string{layout.CapabilityApplication, layout.CapabilityEdge}, hello.Capabilities)

	files, err := Build(fileSharingInput(t))
	require.NoError(t, err)
	require.Equal(t, []string{
		layout.CapabilityApplication,
		layout.CapabilityDatabase,
		layout.CapabilityObjectStorage,
		layout.CapabilityEdge,
	}, files.Capabilities)
}

func TestBuildRejectsInvalidInput(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		mutate  func(*Input)
		message string
		// values marks violations of the values contract, which are typed
		// so the API can surface them as the deployer's mistake.
		values bool
	}{
		"invalid environment": {
			mutate:  func(input *Input) { input.Environment = "Production" },
			message: "invalid environment name",
		},
		"missing artifact": {
			mutate:  func(input *Input) { delete(input.Artifacts, "web") },
			message: "application web has no prepared artifact",
		},
		"invalid digest": {
			mutate: func(input *Input) {
				artifact := input.Artifacts["web"]
				artifact.Digest = "sha256:short"
				input.Artifacts["web"] = artifact
			},
			message: "invalid digest",
		},
		"build artifact with upstream": {
			mutate: func(input *Input) {
				artifact := input.Artifacts["web"]
				artifact.Upstream = "ghcr.io/other/web:1"
				input.Artifacts["web"] = artifact
			},
			message: "must not carry an upstream reference",
		},
		"wrong kind for build source": {
			mutate: func(input *Input) {
				artifact := input.Artifacts["web"]
				artifact.Kind = KindImport
				input.Artifacts["web"] = artifact
			},
			message: "artifact kind must be build-local or build-cloud",
		},
		"unmatched artifact": {
			mutate: func(input *Input) {
				input.Artifacts["ghost"] = Artifact{Reference: "r", Digest: digest("4"), Kind: KindImport}
			},
			message: "artifact ghost does not match any application",
		},
		"secret imported as plain": {
			mutate: func(input *Input) {
				delete(input.Values.Secret, "SESSION_SECRET")
				input.Values.Plain["SESSION_SECRET"] = "oops"
			},
			message: "secret value SESSION_SECRET must not appear in the plain value set",
			values:  true,
		},
		"missing secret": {
			mutate:  func(input *Input) { delete(input.Values.Secret, "SESSION_SECRET") },
			message: "missing secret value SESSION_SECRET",
			values:  true,
		},
		"missing required value": {
			mutate:  func(input *Input) { delete(input.Values.Plain, "APP_DOMAIN") },
			message: "missing required value APP_DOMAIN",
			values:  true,
		},
		"unknown value": {
			mutate:  func(input *Input) { input.Values.Plain["EXTRA"] = "x" },
			message: "value EXTRA is not required by the definition",
			values:  true,
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			input := fileSharingInput(t)
			testCase.mutate(&input)
			_, err := Build(input)
			require.ErrorContains(t, err, testCase.message)
			var valuesErr *ValuesError
			require.Equal(t, testCase.values, errors.As(err, &valuesErr))
		})
	}
}
