package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/checkout"
	"github.com/Hinkolas/skali/internal/cliconfig"
	"github.com/Hinkolas/skali/internal/client"
)

const (
	testSnapshotProduction = "0198f2f4-0000-7000-8000-000000000001"
	testSnapshotStaging    = "0198f2f4-0000-7000-8000-000000000002"
	testSnapshotRetired    = "0198f2f4-0000-7000-8000-000000000003"
)

// seedBackupScope stages a bound checkout whose project holds snapshots of
// two live environments and one the installation no longer knows.
func seedBackupScope(t *testing.T) *fakeInstall {
	t.Helper()
	install := newFakeInstall(t)
	install.seed("p1", "flowdemo", "production", "staging")
	install.backups["p1"] = []client.BackupSnapshot{
		{ID: testSnapshotProduction, Environment: "production", CreatedAt: "2026-08-15T10:00:00Z",
			RevisionChecksum: "sha256:abcdef1234567890", Databases: 1, Buckets: 1, Volumes: 1, Bytes: 12345},
		{ID: testSnapshotStaging, Environment: "staging", CreatedAt: "2026-08-14T09:30:00Z",
			RevisionChecksum: "sha256:0123456789abcdef", Databases: 1, Bytes: 512},
		{ID: testSnapshotRetired, Environment: "retired", CreatedAt: "2026-08-01T08:00:00Z",
			RevisionChecksum: "sha256:fedcba9876543210", Volumes: 2, Bytes: 1 << 20},
	}
	stageRemotes(t, "r", map[string]*cliconfig.Remote{"r": {Master: install.srv.URL}})
	project := testFlowProject(t)
	require.NoError(t, checkout.Save(project.Root, &checkout.Target{
		Master: install.srv.URL, Project: "flowdemo", Environment: "production"}))
	t.Chdir(project.Root)
	return install
}

func runBackupLs(t *testing.T, args ...string) string {
	t.Helper()
	command := newBackupLsCommand()
	command.SetArgs(args)
	out := &bytes.Buffer{}
	command.SetOut(out)
	command.SetErr(out)
	require.NoError(t, command.ExecuteContext(context.Background()))
	return out.String()
}

func TestBackupLsListsWholeProjectWithHeader(t *testing.T) {
	seedBackupScope(t)
	out := runBackupLs(t)

	require.Contains(t, out, "project  flowdemo (r)")
	require.Contains(t, out, "SNAPSHOT")
	require.Contains(t, out, "ENVIRONMENT")
	require.Contains(t, out, "CREATED")
	require.Contains(t, out, "REVISION")
	require.Contains(t, out, "DATABASES  BUCKETS  VOLUMES  SIZE")
	// Every environment's snapshots appear, including one the installation
	// no longer has, and the id leads its row for restore to take.
	require.Regexp(t, `(?m)^`+testSnapshotProduction+`  production `, out)
	require.Regexp(t, `(?m)^`+testSnapshotStaging+`  staging `, out)
	require.Regexp(t, `(?m)^`+testSnapshotRetired+`  retired `, out)
	require.Contains(t, out, "1.0MiB")
}

func TestBackupLsEnvironmentFilters(t *testing.T) {
	seedBackupScope(t)
	out := runBackupLs(t, "--environment", "staging")
	require.Contains(t, out, testSnapshotStaging)
	require.NotContains(t, out, testSnapshotProduction)
	require.NotContains(t, out, testSnapshotRetired)

	out = runBackupLs(t, "--environment", "nothing")
	require.Contains(t, out, "no snapshots of environment nothing")
}

func TestBackupLsProjectFlagOutsideCheckout(t *testing.T) {
	seedBackupScope(t)
	// Outside any checkout the project must be named; the flag the error
	// points at exists on the command.
	t.Chdir(t.TempDir())
	command := newBackupLsCommand()
	command.SetArgs(nil)
	command.SetOut(&bytes.Buffer{})
	err := command.ExecuteContext(context.Background())
	require.ErrorIs(t, err, errProjectRequired)

	out := runBackupLs(t, "--project", "flowdemo")
	require.Contains(t, out, testSnapshotProduction)
	require.Contains(t, out, testSnapshotStaging)
}

func TestBackupCreateConfirmsWithSummary(t *testing.T) {
	install := seedBackupScope(t)
	create := func(stdin string, args ...string) (string, error) {
		command := newBackupCreateCommand()
		command.SetArgs(append([]string{"--detach"}, args...))
		out := &bytes.Buffer{}
		command.SetOut(out)
		command.SetErr(out)
		command.SetIn(strings.NewReader(stdin))
		err := command.ExecuteContext(context.Background())
		return out.String(), err
	}

	// The summary names what would be backed up; declining starts nothing.
	out, err := create("n\n")
	require.ErrorContains(t, err, "aborted")
	require.Contains(t, out, "project      flowdemo")
	require.Contains(t, out, "environment  production")
	require.Contains(t, out, "Back up environment production? [Y/n]")
	require.Empty(t, install.posts)

	// Enter takes the default and starts the backup.
	out, err = create("\n", "--environment", "staging")
	require.NoError(t, err)
	require.Contains(t, out, "environment  staging")
	require.Contains(t, out, "back up staging")
	require.Equal(t, []string{"backup:p1-e2"}, install.posts)

	// Closed stdin without --yes points at the flag; --yes asks nothing.
	_, err = create("")
	require.ErrorContains(t, err, "non-interactive use requires --yes")
	out, err = create("", "--yes")
	require.NoError(t, err)
	require.NotContains(t, out, "[Y/n]")
	require.Len(t, install.posts, 2)
}

func TestBackupRestoreSummaryAndBareInvocation(t *testing.T) {
	seedBackupScope(t)
	restore := func(stdin string, args ...string) (string, error) {
		command := newBackupRestoreCommand()
		command.SetArgs(args)
		out := &bytes.Buffer{}
		command.SetOut(out)
		command.SetErr(out)
		command.SetIn(strings.NewReader(stdin))
		err := command.ExecuteContext(context.Background())
		return out.String(), err
	}

	// A named snapshot prints the resolved summary before the typed
	// confirmation, which a non-terminal cannot answer.
	out, err := restore("", testSnapshotStaging)
	require.ErrorContains(t, err, "non-interactive use requires --yes")
	require.Contains(t, out, "project      flowdemo")
	require.Contains(t, out, "snapshot     "+testSnapshotStaging)
	require.Contains(t, out, "(staging, ")
	require.Contains(t, out, "environment  staging")

	// Without an id and without a terminal the error names the form.
	_, err = restore("")
	require.ErrorContains(t, err, "name the snapshot to restore")
	require.ErrorContains(t, err, "restore <snapshot-id>")
	_, err = restore("", "--environment", "nothing")
	require.ErrorContains(t, err, "no snapshots of environment nothing")

	// The picker rows lead with when and where, and carry the id as value.
	options := snapshotOptions([]client.BackupSnapshot{
		{ID: "a", Environment: "production", CreatedAt: "2026-08-15T10:00:00Z", RevisionChecksum: "sha256:abcdef1234567890", Bytes: 1 << 20},
		{ID: "b", Environment: "qa", CreatedAt: "2026-08-14T10:00:00Z", RevisionChecksum: "sha256:0123456789abcdef", Bytes: 2048},
	})
	require.Len(t, options, 2)
	require.Equal(t, "a", options[0].Value)
	require.Regexp(t, `^2026-08-1\d \d\d:\d\d  production  1\.0MiB$`, options[0].Label)
	require.Regexp(t, `^2026-08-1\d \d\d:\d\d  qa          2\.0KiB$`, options[1].Label)
	require.Equal(t, "abcdef123456", options[0].Description)
}

func TestRestoreEnvironmentDefaultsToSnapshotOrigin(t *testing.T) {
	seedBackupScope(t)
	scope, err := resolveQueryProject(context.Background(), ".", "", "", "")
	require.NoError(t, err)

	// Without --environment the snapshot goes back where it was taken.
	snapshot, err := findSnapshot(context.Background(), scope, testSnapshotStaging)
	require.NoError(t, err)
	target, err := restoreEnvironment(scope, snapshot, "")
	require.NoError(t, err)
	require.Equal(t, "staging", target.Name)
	require.Equal(t, "p1-e2", target.ID)

	// An explicit environment redirects it within the project.
	target, err = restoreEnvironment(scope, snapshot, "production")
	require.NoError(t, err)
	require.Equal(t, "p1-e1", target.ID)
	_, err = restoreEnvironment(scope, snapshot, "missing")
	require.ErrorContains(t, err, "environment missing does not exist in project flowdemo")

	// A snapshot of a retired environment needs an explicit destination.
	snapshot, err = findSnapshot(context.Background(), scope, testSnapshotRetired)
	require.NoError(t, err)
	_, err = restoreEnvironment(scope, snapshot, "")
	require.ErrorContains(t, err, "environment retired, which the snapshot was taken from, does not exist")
	require.ErrorContains(t, err, "--environment")

	_, err = findSnapshot(context.Background(), scope, "nope")
	require.ErrorContains(t, err, "no snapshot nope in project flowdemo")
}
