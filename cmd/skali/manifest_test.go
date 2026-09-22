package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Hinkolas/skali/internal/checkout"
	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/manifest"
	"github.com/stretchr/testify/require"
)

const upgradeFixtureBody = `name: demo
description: fixture

applications:
  web:
    image: example.invalid/web:1
`

func writeManifestFixture(t *testing.T, head string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "skali.yml")
	require.NoError(t, os.WriteFile(path, []byte(head+upgradeFixtureBody), 0600))
	return path
}

func reviewedRevision(t *testing.T, path string) int {
	t.Helper()
	revision, known, err := checkout.Review(path)
	require.NoError(t, err)
	require.True(t, known)
	return revision
}

func TestUpgradeRemovesMetadataAndPreservesComments(t *testing.T) {
	for _, head := range []string{"version: \"1\"  # keep me\n", "skali: v9.9.9  # keep me\n", "version: \"1\"\nskali: v0.1.0  # keep me\n"} {
		path := writeManifestFixture(t, "# header\n"+head)
		require.NoError(t, execute(newRootCommand(), "manifest", "upgrade", "--manifest", path))
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, "# header\n# keep me\n"+upgradeFixtureBody, string(data))
		info, err := os.Stat(path)
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0600), info.Mode().Perm())
		require.Equal(t, manifest.CurrentRevision(), reviewedRevision(t, path))
	}
}

func TestUpgradeStateOnlyAndNewerHistory(t *testing.T) {
	withCLIVersion(t, "v0.0.0-dev")
	path := writeManifestFixture(t, "# preserve exactly\n")
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, execute(newRootCommand(), "manifest", "upgrade", "--manifest", path))
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, before, after)
	_, err = checkout.SaveReview(path, 100, false)
	require.NoError(t, err)
	require.NoError(t, execute(newRootCommand(), "manifest", "upgrade", "--manifest", path))
	require.Equal(t, 100, reviewedRevision(t, path))
	require.Error(t, execute(newRootCommand(), "manifest", "upgrade", "--manifest", path, "--to", "v1.0.0"))
}

func TestUpgradeSemanticAcknowledgement(t *testing.T) {
	original := manifest.CurrentRevision()
	withLedger(t, append(append([]manifest.Change{}, manifest.Ledger...), manifest.Change{
		Revision: original + 1, Kind: manifest.ChangeChanged, Path: "applications.*.deployment.rollout.strategy", WhenOmitted: true, Message: "default changed", Hint: "review strategy",
	}))
	path := writeManifestFixture(t, "skali: v0.1.0\n")
	_, err := checkout.SaveReview(path, original, false)
	require.NoError(t, err)
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	err = execute(newRootCommand(), "manifest", "upgrade", "--manifest", path)
	require.ErrorContains(t, err, "applications.web.deployment.rollout.strategy")
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.Equal(t, original, reviewedRevision(t, path))
	var stderr bytes.Buffer
	root := newRootCommand()
	root.SetErr(&stderr)
	require.NoError(t, execute(root, "manifest", "upgrade", "--manifest", path, "--acknowledge"))
	require.Contains(t, stderr.String(), "default changed")
	require.Equal(t, original+1, reviewedRevision(t, path))
}

func TestUpgradeFailureDoesNotWrite(t *testing.T) {
	for _, body := range []string{"name: BAD\nbuckets:\n  files: {}\n", "name: demo\nunknown: true\n"} {
		path := writeManifestFixture(t, "")
		before := []byte("skali: v0.1.0\n" + body)
		require.NoError(t, os.WriteFile(path, before, 0600))
		require.Error(t, execute(newRootCommand(), "manifest", "upgrade", "--manifest", path, "--acknowledge"))
		after, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, before, after)
		_, known, err := checkout.Review(path)
		require.NoError(t, err)
		require.False(t, known)
	}
}

func TestUpgradeLineEndingsAndUnsafeShapes(t *testing.T) {
	data, _, err := upgradeManifest([]byte("# header\r\nskali: v0.3.0 # keep\r\nname: app\r\n"))
	require.NoError(t, err)
	require.Equal(t, "# header\r\n# keep\r\nname: app\r\n", string(data))
	for _, source := range []string{"skali: v0.1.0\nname: demo\n---\nname: second\n", "{skali: v0.3.0, name: app}", "skali: >\n  v0.3.0\nname: app\n", "skali: &release v0.3.0\nname: app\n", "skali: a\nskali: b\n", "backups: {daily: {}}\nname: app\n"} {
		rewritten, _, err := upgradeManifest([]byte(source))
		require.Error(t, err)
		require.Nil(t, rewritten)
	}
}

func TestUpgradeStripsBackups(t *testing.T) {
	block := "backups:\n  daily:\n    schedule: \"0 3 * * *\"\n    retention: 7d\n"
	for name, tc := range map[string]struct{ in, out string }{
		"middle":           {"name: demo\n\n# snapshots\n" + block + "\nbuckets:\n  files: {}\n", "name: demo\n\nbuckets:\n  files: {}\n"},
		"last":             {"name: demo\nbuckets:\n  files: {}\n\n" + block, "name: demo\nbuckets:\n  files: {}\n"},
		"next comment":     {"name: demo\n\n" + block + "\n# uploads\nbuckets:\n  files: {}\n", "name: demo\n\n# uploads\nbuckets:\n  files: {}\n"},
		"adjacent comment": {"skali: v0.1.0 # preserve\n" + block + "name: demo\nbuckets:\n  files: {}\n", "# preserve\nname: demo\nbuckets:\n  files: {}\n"},
		"adjacent":         {"skali: v0.1.0\n" + block + "version: 1\nname: demo\nbuckets:\n  files: {}\n", "name: demo\nbuckets:\n  files: {}\n"},
	} {
		t.Run(name, func(t *testing.T) {
			for _, ending := range []string{"\n", "\r\n"} {
				data, _, err := upgradeManifest([]byte(strings.ReplaceAll(tc.in, "\n", ending)))
				require.NoError(t, err)
				require.Equal(t, strings.ReplaceAll(tc.out, "\n", ending), string(data))
				doc, err := manifest.Parse(data, "skali.yml")
				require.NoError(t, err)
				require.Empty(t, manifest.Validate(doc))
			}
		})
	}
}

func TestLocalReviewFirstUseAndCompileOutput(t *testing.T) {
	path := writeManifestFixture(t, "")
	var out, stderr bytes.Buffer
	root := newRootCommand()
	root.SetOut(&out)
	root.SetErr(&stderr)
	require.NoError(t, execute(root, "compile", "--manifest", path))
	var result compiler.Result
	require.NoError(t, json.Unmarshal(out.Bytes(), &result))
	require.Equal(t, manifest.CurrentRevision(), reviewedRevision(t, path))
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	_, err = checkout.SaveReview(path, 100, false)
	require.NoError(t, err)
	doc, err := manifest.ParseFile(path)
	require.NoError(t, err)
	compiled, err := compileReviewed(doc, &stderr)
	require.NoError(t, err)
	require.Equal(t, result.Hash, compiled.Hash)
	require.Contains(t, stderr.String(), "retaining local history")
	require.Equal(t, 100, reviewedRevision(t, path))
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestReviewBlocksOnlyKnownAffectedManifests(t *testing.T) {
	withLedger(t, []manifest.Change{{Revision: 1, Kind: manifest.ChangeChanged, Path: "applications.*.image", Message: "image semantics changed", Hint: "review image"}})
	path := writeManifestFixture(t, "")
	_, err := checkout.SaveReview(path, 0, false)
	require.NoError(t, err)
	_, _, err = loadAndCompile(path)
	require.ErrorContains(t, err, "--acknowledge")
	_, err = loadLocalProject(path)
	require.ErrorContains(t, err, "--acknowledge")
	require.Equal(t, 0, reviewedRevision(t, path))
	// In-memory/server compilation has no local history dependency.
	doc, err := manifest.ParseFile(path)
	require.NoError(t, err)
	_, err = compiler.Compile(doc)
	require.NoError(t, err)
	copied := filepath.Join(filepath.Dir(path), "copied.yml")
	require.NoError(t, os.WriteFile(copied, []byte(upgradeFixtureBody), 0600))
	_, _, err = loadAndCompile(copied)
	require.NoError(t, err)
	require.Equal(t, 1, reviewedRevision(t, copied))
	// Successful validation of an unaffected manifest does not advance history.
	require.NoError(t, os.WriteFile(path, []byte("name: demo\nbuckets:\n  files: {}\n"), 0600))
	_, _, err = loadAndCompile(path)
	require.NoError(t, err)
	require.Equal(t, 0, reviewedRevision(t, path))
}

func TestReviewPersistenceFailure(t *testing.T) {
	path := writeManifestFixture(t, "")
	dir := filepath.Join(filepath.Dir(path), ".skali")
	require.NoError(t, os.MkdirAll(dir, 0700))
	// A directory at the stable lock path makes writes fail even as root.
	require.NoError(t, os.Mkdir(filepath.Join(dir, "manifest-review.yaml.lock"), 0700))
	var stderr bytes.Buffer
	_, _, err := loadAndCompile(path, &stderr)
	require.NoError(t, err)
	require.Contains(t, stderr.String(), "warning: could not record")
	require.NoError(t, os.WriteFile(path, []byte("skali: v0.1.0\n"+upgradeFixtureBody), 0600))
	err = execute(newRootCommand(), "manifest", "upgrade", "--manifest", path)
	require.ErrorContains(t, err, "cleanup succeeded, but local acknowledgement was not saved")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, upgradeFixtureBody, string(data))
}

func TestMetadataOperationsDoNotCreateReviewHistory(t *testing.T) {
	path := writeManifestFixture(t, "")
	_, err := readLocalProject(path, nil)
	require.NoError(t, err)
	for _, args := range [][]string{{"validate", "--manifest", path, "--help"}, {"skill", "read", "manifest", "--manifest", path}, {"__complete", "validate", "--manifest", path, ""}} {
		root := newRootCommand()
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&bytes.Buffer{})
		require.NoError(t, execute(root, args...))
	}
	require.NoDirExists(t, filepath.Join(filepath.Dir(path), ".skali"))
}

func TestReviewRejectsCorruptStateWithoutChangingIt(t *testing.T) {
	path := writeManifestFixture(t, "")
	dir := filepath.Join(filepath.Dir(path), ".skali")
	require.NoError(t, os.Mkdir(dir, 0700))
	state := filepath.Join(dir, "manifest-review.yaml")
	require.NoError(t, os.WriteFile(state, []byte("broken: true\n"), 0600))
	_, _, err := loadAndCompile(path)
	require.ErrorContains(t, err, "invalid manifest review state")
	require.ErrorContains(t, execute(newRootCommand(), "manifest", "upgrade", "--manifest", path, "--acknowledge"), "invalid manifest review state")
	data, err := os.ReadFile(state)
	require.NoError(t, err)
	require.Equal(t, "broken: true\n", string(data))
}

func TestInvalidFirstUseDoesNotRecordReview(t *testing.T) {
	path := writeManifestFixture(t, "")
	require.NoError(t, os.WriteFile(path, []byte("name: INVALID\nbuckets:\n  files: {}\n"), 0600))
	_, _, err := loadAndCompile(path)
	require.Error(t, err)
	require.NoDirExists(t, filepath.Join(filepath.Dir(path), ".skali"))
}
