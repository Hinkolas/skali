package build

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// write creates path below root with parents, returning the absolute path.
func write(t *testing.T, root, rel, content string) string {
	t.Helper()
	path := filepath.Join(root, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

func collect(t *testing.T, root, contextRel string, opts CollectOptions) *Context {
	t.Helper()
	collected, err := Collect(root, contextRel, opts)
	require.NoError(t, err)
	return collected
}

func TestCollectDeterminism(t *testing.T) {
	t.Parallel()
	makeTree := func() string {
		root := t.TempDir()
		write(t, root, "web/main.go", "package main")
		write(t, root, "web/assets/logo.svg", "<svg/>")
		write(t, root, "web/README.md", "readme")
		return root
	}
	first := collect(t, makeTree(), "web", CollectOptions{})
	second := collect(t, makeTree(), "web", CollectOptions{})
	require.Equal(t, first.TreeHash, second.TreeHash)
	require.Equal(t, []string{"README.md", "assets/logo.svg", "main.go"}, first.Files)

	// Content changes the hash; so does the executable bit alone.
	root := makeTree()
	write(t, root, "web/main.go", "package main // changed")
	require.NotEqual(t, first.TreeHash, collect(t, root, "web", CollectOptions{}).TreeHash)

	root = makeTree()
	require.NoError(t, os.Chmod(filepath.Join(root, "web/main.go"), 0o755))
	require.NotEqual(t, first.TreeHash, collect(t, root, "web", CollectOptions{}).TreeHash)
}

func TestCollectIgnoreAndSafetyExclusions(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, root, "web/main.go", "package main")
	write(t, root, "web/node_modules/dep/index.js", "dep")
	write(t, root, "web/keep/generated.txt", "keep")
	write(t, root, "web/keep/dropped.log", "drop")
	// The keep directory is ignored wholesale, but the exception pattern
	// re-includes one child, so the walk may not skip the directory.
	write(t, root, "web/.skaliignore", "node_modules\nkeep\n!keep/generated.txt\n")

	// Hard exclusions apply with or without an ignore file.
	write(t, root, "web/.git/HEAD", "ref")
	write(t, root, "web/.skali/state", "state")
	write(t, root, "web/.env", "SECRET=1")
	write(t, root, "web/.env.production", "SECRET=2")
	write(t, root, "web/config/.env", "SECRET=3")
	selected := write(t, root, "web/values.env", "SECRET=4")

	collected := collect(t, root, "web", CollectOptions{ExcludeFiles: []string{selected}})
	require.Equal(t, []string{".skaliignore", "keep/generated.txt", "main.go"}, collected.Files)
}

func TestCollectRejectsEscapes(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, root, "web/main.go", "package main")
	write(t, root, "outside.txt", "outside")

	_, err := Collect(root, "../elsewhere", CollectOptions{})
	require.ErrorIs(t, err, ErrContextEscape)

	// A symlink out of the context is rejected even though the manifest
	// path validation passed; so is an absolute one.
	require.NoError(t, os.Symlink("../../outside.txt", filepath.Join(root, "web", "escape")))
	_, err = Collect(root, "web", CollectOptions{})
	require.ErrorIs(t, err, ErrContextEscape)
	require.NoError(t, os.Remove(filepath.Join(root, "web", "escape")))

	require.NoError(t, os.Symlink("/etc/hosts", filepath.Join(root, "web", "absolute")))
	_, err = Collect(root, "web", CollectOptions{})
	require.ErrorIs(t, err, ErrContextEscape)
	require.NoError(t, os.Remove(filepath.Join(root, "web", "absolute")))

	// A relative in-context symlink is fine and part of the hash.
	require.NoError(t, os.Symlink("main.go", filepath.Join(root, "web", "link.go")))
	collected := collect(t, root, "web", CollectOptions{})
	require.Contains(t, collected.Files, "link.go")
}

func TestConfigAndInputHashes(t *testing.T) {
	t.Parallel()
	dockerfile := []byte("FROM scratch\nCOPY . /\n")
	base := ConfigHash(dockerfile, "", nil)
	require.NotEqual(t, base, ConfigHash([]byte("FROM scratch\n"), "", nil))
	require.NotEqual(t, base, ConfigHash(dockerfile, "runtime", nil))
	require.NotEqual(t, base, ConfigHash(dockerfile, "", map[string]string{"VERSION": "2"}))
	require.Equal(t,
		ConfigHash(dockerfile, "", map[string]string{"A": "1", "B": "2"}),
		ConfigHash(dockerfile, "", map[string]string{"B": "2", "A": "1"}))

	input := InputHash("tree", base, "linux/arm64")
	require.Equal(t, input, InputHash("tree", base, "linux/arm64"))
	require.NotEqual(t, input, InputHash("tree", base, "linux/amd64"))
	require.NotEqual(t, input, InputHash("other", base, "linux/arm64"))
}
