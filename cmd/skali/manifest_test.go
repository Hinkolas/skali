package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/manifest"
)

const upgradeFixtureBody = `name: demo
description: fixture

applications:
  web:
    image: example.invalid/web:1
`

func writeManifestFixture(t *testing.T, head string) string {
	t.Helper()
	directory := t.TempDir()
	path := filepath.Join(directory, "skali.yml")
	require.NoError(t, os.WriteFile(path, []byte(head+upgradeFixtureBody), 0o600))
	return path
}

func TestManifestUpgradeRenamesVersion(t *testing.T) {
	withCLIVersion(t, "v0.1.0-rc.3")
	path := writeManifestFixture(t, "# yaml-language-server: $schema=../schemas/skali.schema.json\n\nversion: \"1\"  # keep me\n")
	out, err := runCapturingStdout(t, func() error {
		return execute(newRootCommand(), "manifest", "upgrade", "--manifest", path)
	})
	require.NoError(t, err)
	require.Equal(t, "upgraded "+path+": version \"1\" -> skali: v0.1.0-rc.3\n", out)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "# yaml-language-server: $schema=../schemas/skali.schema.json\n\nskali: v0.1.0-rc.3  # keep me\n"+upgradeFixtureBody, string(data))
	document, err := manifest.ParseFile(path)
	require.NoError(t, err)
	require.Empty(t, manifest.Validate(document))
}

func TestManifestUpgradeBumpsWatermark(t *testing.T) {
	withCLIVersion(t, "v0.1.0-rc.4")
	path := writeManifestFixture(t, "skali: v0.1.0-rc.3\n")
	out, err := runCapturingStdout(t, func() error {
		return execute(newRootCommand(), "manifest", "upgrade", "--manifest", path)
	})
	require.NoError(t, err)
	require.Equal(t, "upgraded "+path+": skali v0.1.0-rc.3 -> v0.1.0-rc.4\n", out)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(data), "skali: v0.1.0-rc.4\nname: demo\n"), string(data))
}

func TestManifestUpgradeAddsMissingWatermark(t *testing.T) {
	withCLIVersion(t, "v0.0.0-dev")
	path := writeManifestFixture(t, "# a comment\n")
	out, err := runCapturingStdout(t, func() error {
		return execute(newRootCommand(), "manifest", "upgrade", "--manifest", path, "--to", "0.2.0")
	})
	require.NoError(t, err)
	require.Equal(t, "upgraded "+path+": added skali: v0.2.0\nreviewed with working-tree compiler v0.0.0-dev\n", out)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "# a comment\nskali: v0.2.0\n"+upgradeFixtureBody, string(data))
}

func TestManifestUpgradeAlreadyCurrent(t *testing.T) {
	withCLIVersion(t, "v0.1.0-rc.3")
	path := writeManifestFixture(t, "skali: v0.1.0-rc.3\n")
	out, err := runCapturingStdout(t, func() error {
		return execute(newRootCommand(), "manifest", "upgrade", "--manifest", path)
	})
	require.NoError(t, err)
	require.Equal(t, path+" is already reviewed against v0.1.0-rc.3\n", out)

	newer := writeManifestFixture(t, "skali: v0.2.0\n")
	out, err = runCapturingStdout(t, func() error {
		return execute(newRootCommand(), "manifest", "upgrade", "--manifest", newer)
	})
	require.ErrorContains(t, err, "newer than this compiler")
	require.Empty(t, out)
}

func TestManifestUpgradeDevBuildNeedsTo(t *testing.T) {
	withCLIVersion(t, "v0.0.0-dev")
	path := writeManifestFixture(t, "skali: v0.1.0-rc.3\n")
	err := execute(newRootCommand(), "manifest", "upgrade", "--manifest", path)
	require.ErrorContains(t, err, "development build")
	require.ErrorContains(t, err, "--to")
	err = execute(newRootCommand(), "manifest", "upgrade", "--manifest", path, "--to", "latest")
	require.ErrorContains(t, err, `--to "latest" is not a skali release`)
}

func TestReleasedCompilerCannotCertifyAnotherRelease(t *testing.T) {
	withCLIVersion(t, "v0.4.0")
	path := writeManifestFixture(t, "skali: v0.3.0\n")
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	err = execute(newRootCommand(), "manifest", "upgrade", "--manifest", path, "--to", "v0.5.0")
	require.Error(t, err)
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestManifestUpgradePreservesLineEndingsAndRejectsUnsafeShapes(t *testing.T) {
	text := []byte("# header\r\nskali: v0.3.0  # review\r\nname: app\r\n")
	rewritten, _, err := upgradeManifest(text, "v0.4.0")
	require.NoError(t, err)
	require.Equal(t, "# header\r\nskali: v0.4.0  # review\r\nname: app\r\n", string(rewritten))
	for _, source := range []string{"{skali: v0.3.0, name: app}", "skali: >\n  v0.3.0\nname: app\n", "skali: &release v0.3.0\nname: app\n"} {
		rewritten, _, err = upgradeManifest([]byte(source), "v0.4.0")
		require.Error(t, err)
		require.Nil(t, rewritten)
	}
}

// The backups block left the manifest in v0.1.0-rc.8: the upgrade drops it
// with the comment block directly above it, keeps every other comment and
// blank line, and moves the watermark in the same run.
func TestManifestUpgradeStripsBackups(t *testing.T) {
	const block = "backups:\n  daily:\n    schedule: \"0 3 * * *\"\n    retention: 7d\n    include:\n      databases: all\n"
	cases := map[string]struct{ in, out, summary string }{
		"middle with comment": {
			in:      "skali: v0.1.0-rc.7\nname: demo\n\n# Nightly snapshots.\n# Kept for a week.\n" + block + "\nbuckets:\n  files: {}\n",
			out:     "skali: v0.1.0-rc.8\nname: demo\n\nbuckets:\n  files: {}\n",
			summary: "removed backups (automatic backups are an environment setting now: skali backup schedule set), skali v0.1.0-rc.7 -> v0.1.0-rc.8",
		},
		"last key": {
			in:      "skali: v0.1.0-rc.8\nname: demo\n\nbuckets:\n  files: {}\n\n" + block,
			out:     "skali: v0.1.0-rc.8\nname: demo\n\nbuckets:\n  files: {}\n",
			summary: "removed backups (automatic backups are an environment setting now: skali backup schedule set)",
		},
		"last key without trailing newline": {
			in:      "skali: v0.1.0-rc.8\nname: demo\nbuckets:\n  files: {}\n" + strings.TrimSuffix(block, "\n"),
			out:     "skali: v0.1.0-rc.8\nname: demo\nbuckets:\n  files: {}\n",
			summary: "removed backups (automatic backups are an environment setting now: skali backup schedule set)",
		},
		"next key keeps its own comment": {
			in:      "skali: v0.1.0-rc.8\nname: demo\n\n" + block + "\n# Buckets hold uploads.\nbuckets:\n  files: {}\n",
			out:     "skali: v0.1.0-rc.8\nname: demo\n\n# Buckets hold uploads.\nbuckets:\n  files: {}\n",
			summary: "removed backups (automatic backups are an environment setting now: skali backup schedule set)",
		},
		"crlf": {
			in:      strings.ReplaceAll("skali: v0.1.0-rc.7\nname: demo\n\n"+block+"\nbuckets:\n  files: {}\n", "\n", "\r\n"),
			out:     "skali: v0.1.0-rc.8\r\nname: demo\r\n\r\nbuckets:\r\n  files: {}\r\n",
			summary: "removed backups (automatic backups are an environment setting now: skali backup schedule set), skali v0.1.0-rc.7 -> v0.1.0-rc.8",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rewritten, summary, err := upgradeManifest([]byte(tc.in), "v0.1.0-rc.8")
			require.NoError(t, err)
			require.Equal(t, tc.out, string(rewritten))
			require.Equal(t, tc.summary, summary)
			_, err = manifest.Parse(rewritten, "skali.yml")
			require.NoError(t, err, "the stripped manifest parses under the new grammar")
		})
	}

	for _, source := range []string{
		"skali: v0.1.0-rc.8\nname: demo\nbackups: {daily: {schedule: \"0 3 * * *\"}}\nbuckets:\n  files: {}\n",
		"skali: v0.1.0-rc.8\nname: demo\nbackups: &b\n  daily: {}\nbuckets:\n  files: {}\n",
	} {
		rewritten, _, err := upgradeManifest([]byte(source), "v0.1.0-rc.8")
		require.Error(t, err)
		require.Nil(t, rewritten)
	}

	// Through the command: the file is rewritten, parses, and the old
	// manifest is refused with the ledger's removal message beforehand.
	withCLIVersion(t, "v0.1.0-rc.8")
	path := writeManifestFixture(t, "skali: v0.1.0-rc.7\n")
	require.NoError(t, os.WriteFile(path, []byte("skali: v0.1.0-rc.7\n"+upgradeFixtureBody+"\n"+block), 0o600))
	_, err := manifest.ParseFile(path)
	require.ErrorContains(t, err, "backups: removed in v0.1.0-rc.8")
	out, err := runCapturingStdout(t, func() error {
		return execute(newRootCommand(), "manifest", "upgrade", "--manifest", path)
	})
	require.NoError(t, err)
	require.Contains(t, out, "upgraded "+path+": removed backups")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "skali: v0.1.0-rc.8\n"+upgradeFixtureBody, string(data))
}

// A watermark moved past a changed entry the manifest has not absorbed is
// still reported: the upgrade rewrites the file, then compiles it.
func TestManifestUpgradeReportsWhatStillFails(t *testing.T) {
	withCLIVersion(t, "v0.1.0-rc.3")
	path := writeManifestFixture(t, "version: \"1\"\n")
	directory := filepath.Dir(path)
	require.NoError(t, os.WriteFile(path, []byte("version: \"1\"\nname: demo\napplications:\n  web:\n    image: example.invalid/web:1\n    bogus: true\n"), 0o600))
	out, err := runCapturingStdout(t, func() error {
		return execute(newRootCommand(), "manifest", "upgrade", "--manifest", path)
	})
	require.ErrorContains(t, err, "applications.web.bogus: unknown field")
	require.Empty(t, out)
	data, err := os.ReadFile(filepath.Join(directory, "skali.yml"))
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(data), "version: \"1\"\n"))
}

func TestValidatePrintsReviewNote(t *testing.T) {
	withCLIVersion(t, "v0.1.0-rc.4")
	path := writeManifestFixture(t, "skali: v0.1.0-rc.3\n")
	out, err := runCapturingStdout(t, func() error {
		return execute(newRootCommand(), "validate", "--manifest", path)
	})
	require.NoError(t, err)
	require.Contains(t, out, "valid "+path+"\n")
	require.Contains(t, out, "\n  reviewed against v0.1.0-rc.3; this CLI is v0.1.0-rc.4 and nothing this manifest uses changed since\n")

	withCLIVersion(t, "v0.1.0-rc.3")
	out, err = runCapturingStdout(t, func() error {
		return execute(newRootCommand(), "validate", "--manifest", path)
	})
	require.NoError(t, err)
	require.NotContains(t, out, "reviewed against")

	legacy := writeManifestFixture(t, "version: \"1\"\n")
	err = execute(newRootCommand(), "validate", "--manifest", legacy)
	require.ErrorContains(t, err, "version: removed in v0.1.0-rc.3")
	require.ErrorContains(t, err, "run skali manifest upgrade")
}
