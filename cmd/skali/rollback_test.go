package main

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/client"
	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/manifest"
)

func TestResolveRevisionArg(t *testing.T) {
	t.Parallel()
	revisions := []client.RevisionSummary{
		{ID: "0198f2f4-0000-7000-8000-000000000001", Checksum: "abcdef1234567890aaaa"},
		{ID: "0198f2f4-0000-7000-8000-000000000002", Checksum: "abc9991234567890bbbb"},
		{ID: "0198f2f4-0000-7000-8000-000000000003", Checksum: "fedcba0987654321cccc"},
	}

	byID, err := resolveRevisionArg(revisions, "0198f2f4-0000-7000-8000-000000000002")
	require.NoError(t, err)
	require.Equal(t, revisions[1].ID, byID.ID)

	byPrefix, err := resolveRevisionArg(revisions, "fedc")
	require.NoError(t, err)
	require.Equal(t, revisions[2].ID, byPrefix.ID)

	withScheme, err := resolveRevisionArg(revisions, "sha256:fedc")
	require.NoError(t, err)
	require.Equal(t, revisions[2].ID, withScheme.ID)

	_, err = resolveRevisionArg(revisions, "abc")
	require.ErrorContains(t, err, "ambiguous")

	_, err = resolveRevisionArg(revisions, "0000")
	require.ErrorContains(t, err, "no revision matches")
}

func TestChooseRevisionDefaultsToPreviousDeploy(t *testing.T) {
	t.Parallel()
	now := time.Now()
	activeID := "0198f2f4-0000-7000-8000-000000000002"
	revisions := []client.RevisionSummary{
		{ID: "0198f2f4-0000-7000-8000-000000000003", Checksum: "cccc111122223333", CreatedAt: now},
		{ID: activeID, Checksum: "bbbb111122223333", CreatedAt: now.Add(-time.Hour)},
		{ID: "0198f2f4-0000-7000-8000-000000000001", Checksum: "aaaa111122223333", CreatedAt: now.Add(-2 * time.Hour)},
	}
	pointer := &client.Target{TargetRevisionID: &activeID, ActiveRevisionID: &activeID}

	// Plain mode: an empty answer takes the default, which must be the
	// newest revision strictly older than the active one.
	out := &bytes.Buffer{}
	chosen, err := chooseRevision(out, inputReader("\n"), revisions, pointer)
	require.NoError(t, err)
	require.Equal(t, "0198f2f4-0000-7000-8000-000000000001", chosen.ID)
	require.Contains(t, out.String(), "Roll back to which revision?:")
	require.Contains(t, out.String(), "active")
}

func TestDeployFromRejectsBuildFlags(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"--from", "staging", "--rebuild"},
		{"--from", "staging", "--platform", "linux/amd64"},
		{"--from", "staging", "--manifest", "skali.yml"},
		{"--from", "staging", "--build", "local"},
	} {
		command := newDeployCommand()
		command.SetArgs(args)
		command.SetOut(&bytes.Buffer{})
		command.SetErr(&bytes.Buffer{})
		err := command.Execute()
		require.ErrorContains(t, err,
			"--from cannot be combined with --build, --platform, --rebuild, or --manifest",
			"args: %v", args)
	}
}

func TestHealthHints(t *testing.T) {
	t.Parallel()
	compile := func(t *testing.T, source string) *compiler.Result {
		t.Helper()
		document, err := manifest.Parse([]byte(source), "skali.yml")
		require.NoError(t, err)
		result, err := compiler.Compile(document)
		require.NoError(t, err)
		return result
	}

	bare := compile(t, `
version: "1"
name: hints
applications:
  worker:
    image: example.invalid/worker:1
  web:
    image: example.invalid/web:1
    ports:
      http:
        port: 8080
    health:
      readiness:
        http:
          port: http
          path: /healthz
`)
	hints := healthHints(bare)
	require.Equal(t, []string{
		"hint: application worker declares no health check; rollouts cannot verify readiness",
	}, hints)

	probed := compile(t, `
version: "1"
name: hints
applications:
  web:
    image: example.invalid/web:1
    ports:
      http:
        port: 8080
    health:
      readiness:
        http:
          port: http
          path: /healthz
`)
	require.Empty(t, healthHints(probed))
}
