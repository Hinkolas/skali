package cluster

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/engine/enginetest"
)

func TestPollerOnlineOfflineTransitions(t *testing.T) {
	ctx := context.Background()
	st, ca, masterAddr := startMaster(t)

	self, err := EnsureSelfNode(ctx, st, "")
	require.NoError(t, err)

	// Enroll a worker whose agent listens on a random port; fix the
	// advertise addr to that port after listening.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	token, _, err := MintJoinToken(ctx, st, ca, []string{"worker"}, nil)
	require.NoError(t, err)
	opts := enrollOptions(t, masterAddr, token)
	opts.AdvertiseAddr = lis.Addr().String()
	identity, _, err := RunEnroll(ctx, opts)
	require.NoError(t, err)

	_, containers := testContainerDeps(t)
	agent := NewAgentServer(identity, warmSampler(t), enginetest.New(), containers)
	go agent.Serve(lis) //nolint:errcheck
	t.Cleanup(agent.Stop)

	poller, err := NewPoller(st, ca, self.ID, warmSampler(t), 25*time.Millisecond, 100*time.Millisecond)
	require.NoError(t, err)
	pollCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go poller.Run(pollCtx)

	nodeStatus := func() (worker string, master string) {
		nodes, err := st.ListNodes(ctx)
		require.NoError(t, err)
		for _, n := range nodes {
			switch n.ID {
			case identity.NodeID:
				worker = n.Status
			case self.ID:
				master = n.Status
			}
		}
		return
	}

	// Worker flips online and facts land; the master's own row is stamped
	// locally without gRPC.
	require.Eventually(t, func() bool {
		w, m := nodeStatus()
		return w == "online" && m == "online"
	}, 5*time.Second, 20*time.Millisecond)
	node, err := st.GetNodeByID(ctx, identity.NodeID)
	require.NoError(t, err)
	require.NotNil(t, node.Arch)
	require.NotNil(t, node.LastSeen)

	// Metrics flow: latest values land on both rows (the agent's warm sampler
	// via gRPC, the master's via the local self-stamp) and history rows
	// accumulate for charting.
	require.NotNil(t, node.MemTotal)
	require.NotNil(t, node.CpuPct)
	master, err := st.GetNodeByID(ctx, self.ID)
	require.NoError(t, err)
	require.NotNil(t, master.MemTotal)
	var historyRows int
	require.NoError(t, st.Pool.QueryRow(ctx,
		"SELECT count(*) FROM node_metrics WHERE node_id = $1", identity.NodeID).Scan(&historyRows))
	require.Positive(t, historyRows)
	samples, err := st.ListNodeMetricsBucketed(ctx, identity.NodeID)
	require.NoError(t, err)
	require.NotEmpty(t, samples)

	// Kill the agent: after the staleness threshold the worker flips
	// offline; the master stays online.
	agent.Stop()
	require.Eventually(t, func() bool {
		w, m := nodeStatus()
		return w == "offline" && m == "online"
	}, 5*time.Second, 20*time.Millisecond)
}

func TestPollerRefusesStaleCertSerial(t *testing.T) {
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
	agent := NewAgentServer(identity, warmSampler(t), enginetest.New(), containers)
	go agent.Serve(lis) //nolint:errcheck
	t.Cleanup(agent.Stop)

	// Invalidate the recorded serial, as a delete/re-enroll would: the
	// poller must refuse the old cert during the handshake.
	stale := "deadbeef"
	_, err = st.Pool.Exec(ctx, "UPDATE nodes SET cert_serial = $1 WHERE id = $2", stale, identity.NodeID)
	require.NoError(t, err)
	node, err := st.GetNodeByID(ctx, identity.NodeID)
	require.NoError(t, err)

	poller, err := NewPoller(st, ca, self.ID, warmSampler(t), time.Hour, time.Hour)
	require.NoError(t, err)
	err = poller.heartbeat(ctx, node)
	require.Error(t, err)
	require.Contains(t, err.Error(), "serial")
}
