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
	require.Equal(t, "upgraded "+path+": added skali: v0.2.0\n", out)
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
	require.NoError(t, err)
	require.Equal(t, newer+" is reviewed against v0.2.0, newer than v0.1.0-rc.3; nothing to do\n", out)
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
	require.Equal(t, "upgraded "+path+": version \"1\" -> skali: v0.1.0-rc.3\n", out)
	data, err := os.ReadFile(filepath.Join(directory, "skali.yml"))
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(data), "skali: v0.1.0-rc.3\n"))
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
