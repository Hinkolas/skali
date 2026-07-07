package cluster

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Hinkolas/skali/internal/clusterpb"
	"github.com/Hinkolas/skali/internal/engine"
	"github.com/Hinkolas/skali/internal/engine/enginetest"
)

// serveWatchAgent starts a NodeService for identity on lis with a live
// notifier over fake, so WatchEvents streams real doorbells.
func serveWatchAgent(t *testing.T, identity *Identity, lis net.Listener, fake *enginetest.Fake) *grpc.Server {
	t.Helper()
	ctx := context.Background()
	containers := engine.NewSampler(fake)
	containers.SampleNow(ctx)
	inventory := engine.NewInventorySampler(fake)
	inventory.SampleNow(ctx)
	notifier := engine.NewNotifier(fake, containers, inventory)
	notifierCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go notifier.Run(notifierCtx)
	agent := NewAgentServer(identity, warmSampler(t), fake, containers, inventory, notifier)
	go agent.Serve(lis) //nolint:errcheck
	t.Cleanup(agent.Stop)
	return agent
}

// emitUntilPoked emits engine events until a poke for want arrives — the
// watcher's stream setup is asynchronous, and re-ringing a doorbell is
// idempotent by design.
func emitUntilPoked(t *testing.T, fake *enginetest.Fake, pokes <-chan uuid.UUID, want uuid.UUID) {
	t.Helper()
	require.Eventually(t, func() bool {
		fake.Emit(engine.EventContainer)
		select {
		case id := <-pokes:
			return id == want
		case <-time.After(700 * time.Millisecond): // past the notifier debounce
			return false
		}
	}, 15*time.Second, 10*time.Millisecond, "doorbell never reached the master")
}

// TestWatcherPokesOnEngineEvents drives the full doorbell path over real
// mTLS: engine event → notifier resample+ring → WatchEvents stream → watcher
// → poke.
func TestWatcherPokesOnEngineEvents(t *testing.T) {
	ctx := context.Background()
	st, ca, masterAddr := startMaster(t)

	self, err := EnsureSelfNode(ctx, st, "")
	require.NoError(t, err)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	token, _, err := MintJoinToken(ctx, st, ca, []string{"worker"}, nil)
	require.NoError(t, err)
	opts := enrollOptions(t, masterAddr, token)
	opts.AdvertiseAddr = lis.Addr().String()
	identity, _, err := RunEnroll(ctx, opts)
	require.NoError(t, err)

	fake := enginetest.New()
	serveWatchAgent(t, identity, lis, fake)

	conns, err := NewConnPool(ca)
	require.NoError(t, err)
	t.Cleanup(conns.Close)
	pokes := make(chan uuid.UUID, 16)
	watcher := NewWatcher(st, conns, self.ID, func(id uuid.UUID) { pokes <- id })
	watchCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go watcher.Run(watchCtx)

	emitUntilPoked(t, fake, pokes, identity.NodeID)
}

// TestWatcherSurvivesAgentRestart kills the agent and brings it back on a
// NEW address: the watcher's per-attempt row re-fetch plus the pool's
// re-dial must find it again without a restart of anything master-side.
func TestWatcherSurvivesAgentRestart(t *testing.T) {
	ctx := context.Background()
	st, ca, masterAddr := startMaster(t)

	self, err := EnsureSelfNode(ctx, st, "")
	require.NoError(t, err)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	token, _, err := MintJoinToken(ctx, st, ca, []string{"worker"}, nil)
	require.NoError(t, err)
	opts := enrollOptions(t, masterAddr, token)
	opts.AdvertiseAddr = lis.Addr().String()
	identity, _, err := RunEnroll(ctx, opts)
	require.NoError(t, err)

	fake := enginetest.New()
	agent := serveWatchAgent(t, identity, lis, fake)

	conns, err := NewConnPool(ca)
	require.NoError(t, err)
	t.Cleanup(conns.Close)
	pokes := make(chan uuid.UUID, 16)
	watcher := NewWatcher(st, conns, self.ID, func(id uuid.UUID) { pokes <- id })
	watchCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go watcher.Run(watchCtx)

	emitUntilPoked(t, fake, pokes, identity.NodeID)

	// Take the agent down and bring it back elsewhere, as a redeploy would.
	agent.Stop()
	lis2, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	_, err = st.Pool.Exec(ctx, "UPDATE nodes SET advertise_addr = $1 WHERE id = $2",
		lis2.Addr().String(), identity.NodeID)
	require.NoError(t, err)
	serveWatchAgent(t, identity, lis2, fake)

	emitUntilPoked(t, fake, pokes, identity.NodeID)
}

// TestPollerPoke pins the poke contract: a poked node's row advances without
// waiting for a tick, and a self-poke stamps the master locally.
func TestPollerPoke(t *testing.T) {
	ctx := context.Background()
	st, ca, masterAddr := startMaster(t)

	self, err := EnsureSelfNode(ctx, st, "")
	require.NoError(t, err)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	token, _, err := MintJoinToken(ctx, st, ca, []string{"worker"}, nil)
	require.NoError(t, err)
	opts := enrollOptions(t, masterAddr, token)
	opts.AdvertiseAddr = lis.Addr().String()
	identity, _, err := RunEnroll(ctx, opts)
	require.NoError(t, err)

	_, containers := testContainerDeps(t)
	agent := NewAgentServer(identity, warmSampler(t), enginetest.New(), containers, nil, nil)
	go agent.Serve(lis) //nolint:errcheck
	t.Cleanup(agent.Stop)

	conns, err := NewConnPool(ca)
	require.NoError(t, err)
	t.Cleanup(conns.Close)
	// Hour-long interval: after the initial tick, only pokes can move rows.
	poller, err := NewPoller(st, conns, self.ID, warmSampler(t), nil, nil, time.Hour, time.Hour)
	require.NoError(t, err)
	pollCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go poller.Run(pollCtx)

	lastSeen := func(id uuid.UUID) *time.Time {
		node, err := st.GetNodeByID(ctx, id)
		require.NoError(t, err)
		return node.LastSeen
	}
	require.Eventually(t, func() bool {
		return lastSeen(identity.NodeID) != nil && lastSeen(self.ID) != nil
	}, 5*time.Second, 20*time.Millisecond, "initial tick stamps both rows")

	workerBefore, selfBefore := *lastSeen(identity.NodeID), *lastSeen(self.ID)
	time.Sleep(20 * time.Millisecond) // guarantee a visible timestamp delta

	poller.Poke(identity.NodeID)
	require.Eventually(t, func() bool {
		return lastSeen(identity.NodeID).After(workerBefore)
	}, 5*time.Second, 20*time.Millisecond, "poked worker resyncs without a tick")
	require.Equal(t, selfBefore, *lastSeen(self.ID), "worker poke leaves the master row alone")

	poller.Poke(self.ID)
	require.Eventually(t, func() bool {
		return lastSeen(self.ID).After(selfBefore)
	}, 5*time.Second, 20*time.Millisecond, "self poke stamps locally")
}

func TestWatchEventsUnavailableWithoutNotifier(t *testing.T) {
	ctx := context.Background()
	client, _, _ := startAgentWithEngine(t, enginetest.New())

	stream, err := client.WatchEvents(ctx, &clusterpb.WatchEventsRequest{})
	require.NoError(t, err)
	_, err = stream.Recv()
	require.Equal(t, codes.Unavailable, status.Code(err))
}
