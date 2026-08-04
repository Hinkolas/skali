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
		Result:         compileExample(t, "hello-world"),
		Environment:    "production",
		SecretVersions: map[string]int{"APP_DOMAIN": 1},
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
		SecretVersions: map[string]int{
			"APP_DOMAIN":     1,
			"SESSION_SECRET": 1,
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
	changed.SecretVersions["APP_DOMAIN"] = 2
	third, err := Build(changed)
	require.NoError(t, err)
	require.NotEqual(t, first.ValuesHash, third.ValuesHash)
	require.NotEqual(t, first.Checksum, third.Checksum)
}

func TestValuePlaintextNeverEntersTheRevision(t *testing.T) {
	t.Parallel()
	built, err := Build(fileSharingInput(t))
	require.NoError(t, err)
	data, err := json.Marshal(built)
	require.NoError(t, err)
	// Every value is a (name, version) reference; no plaintext exists to
	// leak, and the document never gains a values map.
	require.NotContains(t, string(data), "\"values\":")
	require.Equal(t, SecretRef{Version: 1}, built.Secrets["SESSION_SECRET"])
}

func TestValueVersionsAreRecorded(t *testing.T) {
	t.Parallel()
	input := fileSharingInput(t)
	input.SecretVersions["SESSION_SECRET"] = 4
	built, err := Build(input)
	require.NoError(t, err)
	require.Equal(t, SecretRef{Version: 4}, built.Secrets["SESSION_SECRET"])
}

// A stored value the definition no longer references is intersected away:
// it neither fails the build nor enters the revision. This is the fix for
// the wedged-environment failure mode.
func TestOrphanedValuesAreIgnored(t *testing.T) {
	t.Parallel()
	input := fileSharingInput(t)
	input.SecretVersions["REMOVED"] = 3
	built, err := Build(input)
	require.NoError(t, err)
	require.NotContains(t, built.Secrets, "REMOVED")

	clean, err := Build(fileSharingInput(t))
	require.NoError(t, err)
	require.Equal(t, clean.Checksum, built.Checksum)
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
		"missing required value": {
			mutate:  func(input *Input) { delete(input.SecretVersions, "APP_DOMAIN") },
			message: "missing required values: APP_DOMAIN",
			values:  true,
		},
		"missing several required values": {
			mutate: func(input *Input) {
				delete(input.SecretVersions, "APP_DOMAIN")
				delete(input.SecretVersions, "SESSION_SECRET")
			},
			message: "missing required values: APP_DOMAIN, SESSION_SECRET",
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
