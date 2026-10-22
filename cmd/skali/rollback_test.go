package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

// A rollback asks plainly first; one the daemon refuses as destructive is
// shown and sent again confirmed only with --allow-destructive, since a
// non-interactive --yes must not confirm it.
func TestSetRollbackTargetConfirmsDestructive(t *testing.T) {
	t.Parallel()
	const message = "the rollback is destructive: databases.main (deletes the logical database and its data); review it and explicitly allow it"
	serve := func(destructive bool, bodies *[]map[string]any) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			*bodies = append(*bodies, body)
			w.Header().Set("Content-Type", "application/json")
			if destructive && body["allow_destructive"] != true {
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error": map[string]string{"code": "destructive_change", "message": message}})
				return
			}
			_, _ = w.Write([]byte(`{"target":{"target_revision_id":"rev-1","updated_at":"2026-10-22T00:00:00Z"},"run_id":"run-1"}`))
		}))
	}
	rollback := func(destructive bool, opts *rollbackOptions) (*client.SetTargetResult, string, []map[string]any, error) {
		var bodies []map[string]any
		srv := serve(destructive, &bodies)
		defer srv.Close()
		out := &bytes.Buffer{}
		result, err := setRollbackTarget(context.Background(), out,
			client.New(srv.URL, "token", client.Caller{UserAgent: "test"}), "env-1", "production", "rev-1", opts)
		return result, out.String(), bodies, err
	}

	result, out, bodies, err := rollback(false, &rollbackOptions{Yes: true})
	require.NoError(t, err)
	require.Equal(t, "run-1", result.RunID)
	require.Len(t, bodies, 1)
	require.NotContains(t, bodies[0], "allow_destructive")
	require.Empty(t, out)

	_, out, bodies, err = rollback(true, &rollbackOptions{Yes: true})
	require.ErrorContains(t, err, "re-run with --allow-destructive")
	require.Len(t, bodies, 1)
	require.Contains(t, out, "databases.main (deletes the logical database and its data)")

	result, out, bodies, err = rollback(true, &rollbackOptions{Yes: true, AllowDestructive: true})
	require.NoError(t, err)
	require.Equal(t, "run-1", result.RunID)
	require.Len(t, bodies, 2)
	require.NotContains(t, bodies[0], "allow_destructive")
	require.Equal(t, true, bodies[1]["allow_destructive"])
	require.Contains(t, out, "DESTRUCTIVE:")
}
