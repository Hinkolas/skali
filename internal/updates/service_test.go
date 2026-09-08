package updates

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/Hinkolas/skali/internal/clusterstate"
	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/layout"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/testdb"
)

type staticFeed struct {
	release *Release
	err     error
	calls   int
}

func (f *staticFeed) Latest(context.Context, Channel) (*Release, error) {
	f.calls++
	return f.release, f.err
}

func TestServiceScansAndPersistsSettings(t *testing.T) {
	st := store.NewStore(testdb.New(t))
	ctx := context.Background()
	feed := &staticFeed{release: &Release{
		Version: "v0.2.0", PublishedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), URL: "u", K3s: "v1.36.3+k3s1",
	}}
	svc := &Service{Store: st, Feed: feed, Version: "v0.1.0"}

	status, err := svc.Status(ctx)
	require.NoError(t, err)
	require.Equal(t, ChannelStable, status.Channel)
	require.False(t, status.AutoUpdate)
	require.Nil(t, status.Latest)
	require.False(t, status.Managed)
	require.Contains(t, status.Reason, "skali cluster upgrade")

	status, err = svc.Scan(ctx)
	require.NoError(t, err)
	require.NotNil(t, status.LastCheckedAt)
	require.Equal(t, "v0.2.0", status.Latest.Version)
	require.Equal(t, "v1.36.3+k3s1", status.Latest.K3s)
	require.True(t, status.UpdateAvailable)
	require.Empty(t, status.LastError)

	// A feed failure is recorded, and the last release stays visible.
	feed.err = errors.New("HTTP 503")
	status, err = svc.Scan(ctx)
	require.NoError(t, err)
	require.Equal(t, "HTTP 503", status.LastError)
	require.Equal(t, "v0.2.0", status.Latest.Version)
	feed.err = nil

	// A channel change rescans; auto-update persists.
	calls := feed.calls
	status, err = svc.UpdateSettings(ctx, ChannelBeta, true)
	require.NoError(t, err)
	require.Equal(t, ChannelBeta, status.Channel)
	require.True(t, status.AutoUpdate)
	require.Equal(t, calls+1, feed.calls)
	_, err = svc.UpdateSettings(ctx, Channel("nightly"), true)
	require.Error(t, err)

	// A dev build never reports an update as available, and a release
	// that is not newer is not one either.
	dev := &Service{Store: st, Feed: feed, Version: "v0.0.0-dev"}
	status, err = dev.Status(ctx)
	require.NoError(t, err)
	require.False(t, status.UpdateAvailable)
	current := &Service{Store: st, Feed: feed, Version: "v0.2.0"}
	status, err = current.Status(ctx)
	require.NoError(t, err)
	require.False(t, status.UpdateAvailable)

	// Apply on an unmanaged installation is blocked, not an internal error.
	_, err = svc.Apply(ctx, "v0.2.0")
	var blocked *BlockedError
	require.ErrorAs(t, err, &blocked)
	_, err = svc.Apply(ctx, "v0.0.1")
	require.ErrorIs(t, err, ErrNotNewer)

	disabled := &Service{Store: st, Version: "v0.1.0"}
	_, err = disabled.Scan(ctx)
	require.ErrorIs(t, err, ErrScanDisabled)
}

// managedCluster bootstraps a one-server reconciled cluster state in a fake
// clientset, converged at v0.1.0 with the platform enabled.
func managedCluster(t *testing.T, now time.Time) *Cluster {
	t.Helper()
	client := fake.NewSimpleClientset()
	cs := &clusterstate.Store{Client: client, Now: func() time.Time { return now }}
	state, err := clusterstate.NewSeedState("test", clusterstate.Node{
		ID: "seed", InstallationID: "install", Name: "seed", Role: layout.RoleServer,
		Capabilities: append([]string(nil), layout.Capabilities...),
		K3sVersion:   "v1.36.3+k3s1", AgentVersion: "v0.1.0",
	}, now)
	require.NoError(t, err)
	_, err = state.EditCandidate(now, func(_ map[string]clusterstate.RevisionNode, platform *clusterstate.PlatformState) error {
		platform.Enabled = true
		platform.RegistryNode = "seed"
		platform.Version = "v0.1.0"
		return nil
	})
	require.NoError(t, err)
	state.ConvergedRevision = state.CandidateRevision
	state.Platform = state.Revisions[state.ConvergedRevision].Platform
	node := state.Nodes["seed"]
	node.LastSeen = now
	state.Nodes["seed"] = node
	_, err = cs.Bootstrap(context.Background(), state)
	require.NoError(t, err)
	return &Cluster{Client: client, now: func() time.Time { return now }}
}

func TestServiceAppliesThroughClusterState(t *testing.T) {
	st := store.NewStore(testdb.New(t))
	ctx := context.Background()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	feed := &staticFeed{release: &Release{Version: "v0.2.0", PublishedAt: now}}
	svc := &Service{Store: st, Feed: feed, Cluster: managedCluster(t, now), Version: "v0.1.0",
		now: func() time.Time { return now }}

	status, err := svc.Scan(ctx)
	require.NoError(t, err)
	require.True(t, status.Managed)
	require.True(t, status.Manageable, status.Reason)
	require.Len(t, status.Nodes, 1)
	require.Equal(t, "v0.1.0", status.Installed.PlatformVersion)
	require.Nil(t, status.Operation)

	// In-flight work blocks the roll.
	journalSvc := journal.NewService(st, uuid.NewString())
	run, err := journalSvc.CreateRun(ctx, journal.RunInput{Kind: "deployment", Actor: "test"})
	require.NoError(t, err)
	require.NoError(t, journalSvc.StartRun(ctx, run.ID))
	_, err = svc.Apply(ctx, "v0.2.0")
	require.ErrorIs(t, err, ErrBusy)
	require.NoError(t, journalSvc.FinishRun(ctx, run.ID, journal.RunSucceeded))

	status, err = svc.Apply(ctx, "v0.2.0")
	require.NoError(t, err)
	require.NotNil(t, status.Operation)
	require.Equal(t, "v0.2.0", status.Operation.TargetVersion)
	require.Equal(t, "v0.1.0", status.Operation.FromVersion)
	require.Equal(t, clusterstate.OperationPending, status.Operation.Phase)
	require.Len(t, status.Operation.Steps, 1)
	require.Equal(t, "seed", status.Operation.Steps[0].Node)
	require.Equal(t, clusterstate.NodeActionUpgrade, status.Operation.Steps[0].Action)
	require.False(t, status.Manageable)

	_, err = svc.Apply(ctx, "v0.3.0")
	require.ErrorIs(t, err, clusterstate.ErrOperationActive)
	_, err = svc.Resume(ctx)
	require.ErrorIs(t, err, clusterstate.ErrOperationActive, "only a failed operation resumes")

	// The loop applies on its own when asked to, and stays quiet otherwise.
	// The loop's own clock stays real so the scan is due against the row's
	// database timestamp; the cluster clock stays pinned to keep the node
	// heartbeat fresh.
	auto := &Service{Store: st, Feed: &staticFeed{release: &Release{Version: "v0.3.0", PublishedAt: now}},
		Cluster: managedCluster(t, now), Version: "v0.1.0"}
	_, err = auto.UpdateSettings(ctx, ChannelStable, true)
	require.NoError(t, err)
	// UpdateSettings kept the channel, so it did not rescan; a tiny interval
	// makes the loop's next tick due immediately.
	auto.ScanInterval = time.Nanosecond
	auto.tick(ctx)
	status, err = auto.Status(ctx)
	require.NoError(t, err)
	require.NotNil(t, status.Operation)
	require.Equal(t, "v0.3.0", status.Operation.TargetVersion)
}
