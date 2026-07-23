package checkout

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadMissingIsNil(t *testing.T) {
	target, err := Load(t.TempDir())
	require.NoError(t, err)
	require.Nil(t, target)
}

func TestSaveLoadRoundtrip(t *testing.T) {
	root := t.TempDir()
	want := &Target{
		Master:      "https://skali.example.com",
		Project:     "file-sharing",
		Environment: "production",
	}
	require.NoError(t, Save(root, want))

	raw, err := os.ReadFile(Path(root))
	require.NoError(t, err)
	require.Contains(t, string(raw), "# Skali checkout binding")
	info, err := os.Stat(Path(root))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o644), info.Mode().Perm())

	ignore, err := os.ReadFile(filepath.Join(Dir(root), ".gitignore"))
	require.NoError(t, err)
	require.Equal(t, "*\n", string(ignore))

	got, err := Load(root)
	require.NoError(t, err)
	require.Equal(t, want, got)
}

// A customized .skali/.gitignore survives a re-save.
func TestSaveKeepsExistingGitignore(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(Dir(root), 0o755))
	custom := "*\n!target.yaml\n"
	require.NoError(t, os.WriteFile(filepath.Join(Dir(root), ".gitignore"), []byte(custom), 0o644))

	require.NoError(t, Save(root, &Target{Master: "https://m", Project: "p", Environment: "e"}))

	ignore, err := os.ReadFile(filepath.Join(Dir(root), ".gitignore"))
	require.NoError(t, err)
	require.Equal(t, custom, string(ignore))
}

func TestLoadCorrupt(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(Dir(root), 0o755))
	require.NoError(t, os.WriteFile(Path(root), []byte("{not yaml"), 0o644))

	_, err := Load(root)
	require.ErrorContains(t, err, "delete the file to relink this checkout")
}

func TestLoadIncomplete(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(Dir(root), 0o755))
	require.NoError(t, os.WriteFile(Path(root),
		[]byte("master: https://skali.example.com\nproject: p\n"), 0o644))

	_, err := Load(root)
	require.ErrorContains(t, err, "incomplete")
	require.ErrorContains(t, err, "delete the file to relink this checkout")
}
