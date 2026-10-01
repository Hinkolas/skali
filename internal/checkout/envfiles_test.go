package checkout

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadEnvFilesMissingIsEmpty(t *testing.T) {
	environments, err := LoadEnvFiles(t.TempDir())
	require.NoError(t, err)
	require.Empty(t, environments)
}

func TestSaveEnvFileChoiceRoundtrip(t *testing.T) {
	root := t.TempDir()
	// An absolute path is stored relative to the root.
	require.NoError(t, SaveEnvFileChoice(root, "staging", EnvFileChoice{File: filepath.Join(root, ".env.staging")}))
	require.NoError(t, SaveEnvFileChoice(root, "production", EnvFileChoice{Stored: true}))

	raw, err := os.ReadFile(EnvFilesPath(root))
	require.NoError(t, err)
	require.Contains(t, string(raw), "# Skali env file memory")
	require.Contains(t, string(raw), "file: .env.staging\n")
	require.Contains(t, string(raw), "stored: true\n")
	require.NotContains(t, string(raw), root)

	ignore, err := os.ReadFile(filepath.Join(Dir(root), ".gitignore"))
	require.NoError(t, err)
	require.Equal(t, "*\n", string(ignore))

	environments, err := LoadEnvFiles(root)
	require.NoError(t, err)
	require.Equal(t, map[string]EnvFileChoice{
		"staging":    {File: ".env.staging"},
		"production": {Stored: true},
	}, environments)
	require.Equal(t, filepath.Join(root, ".env.staging"), environments["staging"].Resolve(root))
	require.Empty(t, environments["production"].Resolve(root))
}

// Re-picking keeps the recorded fingerprint; a fingerprint is recorded only
// for an environment that has a choice, and never changes the choice.
func TestSaveEnvFileChoiceKeepsFingerprint(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, SaveEnvFileFingerprint(root, "staging", "sha256:early"))
	require.NoFileExists(t, EnvFilesPath(root))

	require.NoError(t, SaveEnvFileChoice(root, "staging", EnvFileChoice{File: ".env.staging"}))
	require.NoError(t, SaveEnvFileFingerprint(root, "staging", "sha256:abc"))
	require.NoError(t, SaveEnvFileFingerprint(root, "other", "sha256:ignored"))
	require.NoError(t, SaveEnvFileChoice(root, "staging", EnvFileChoice{File: ".env"}))

	environments, err := LoadEnvFiles(root)
	require.NoError(t, err)
	require.Equal(t, map[string]EnvFileChoice{
		"staging": {File: ".env", Fingerprint: "sha256:abc"},
	}, environments)

	require.NoError(t, SaveEnvFileChoice(root, "staging", EnvFileChoice{Stored: true, Fingerprint: "sha256:new"}))
	environments, err = LoadEnvFiles(root)
	require.NoError(t, err)
	require.Equal(t, EnvFileChoice{Stored: true, Fingerprint: "sha256:new"}, environments["staging"])

	// An empty fingerprint clears the record (a stored-values deployment).
	require.NoError(t, SaveEnvFileFingerprint(root, "staging", ""))
	environments, err = LoadEnvFiles(root)
	require.NoError(t, err)
	require.Equal(t, EnvFileChoice{Stored: true}, environments["staging"])
}

func TestSaveEnvFileChoiceRejectsBadShapes(t *testing.T) {
	root := t.TempDir()
	require.Error(t, SaveEnvFileChoice(root, "", EnvFileChoice{Stored: true}))
	require.Error(t, SaveEnvFileChoice(root, "staging", EnvFileChoice{}))
	require.Error(t, SaveEnvFileChoice(root, "staging", EnvFileChoice{File: ".env", Stored: true}))
	require.Error(t, SaveEnvFileChoice(root, "staging", EnvFileChoice{File: "../.env"}))
	require.Error(t, SaveEnvFileChoice(root, "staging", EnvFileChoice{File: filepath.Join(filepath.Dir(root), ".env")}))
}

func TestLoadEnvFilesRejectsCorruption(t *testing.T) {
	for _, data := range []string{
		"[",
		"schemaVersion: 2\nenvironments: {}\n",
		"schemaVersion: 1\nenvironments:\n  staging: {}\n",
		"schemaVersion: 1\nenvironments:\n  staging: {file: .env, stored: true}\n",
		"schemaVersion: 1\nenvironments:\n  staging: {file: ../.env}\n",
		"schemaVersion: 1\nenvironments:\n  staging: {file: /etc/passwd}\n",
		"schemaVersion: 1\nenvironments:\n  staging: {file: .env, extra: 1}\n",
		"schemaVersion: 1\nenvironments: {}\n---\n{}",
	} {
		root := t.TempDir()
		require.NoError(t, EnsureDir(root))
		require.NoError(t, os.WriteFile(EnvFilesPath(root), []byte(data), 0o600))
		_, err := LoadEnvFiles(root)
		require.Error(t, err, data)
		require.ErrorContains(t, err, "delete it to pick again", data)
	}
}
