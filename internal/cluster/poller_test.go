package cluster

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/engine"
	"github.com/Hinkolas/skali/internal/engine/enginetest"
	"github.com/Hinkolas/skali/internal/store"
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
	agent := NewAgentServer(identity, warmSampler(t), enginetest.New(), containers, nil)
	go agent.Serve(lis) //nolint:errcheck
	t.Cleanup(agent.Stop)

	conns, err := NewConnPool(ca)
	require.NoError(t, err)
	t.Cleanup(conns.Close)
	poller, err := NewPoller(st, conns, self.ID, warmSampler(t), nil, 25*time.Millisecond, 100*time.Millisecond)
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
	agent := NewAgentServer(identity, warmSampler(t), enginetest.New(), containers, nil)
	go agent.Serve(lis) //nolint:errcheck
	t.Cleanup(agent.Stop)

	// Invalidate the recorded serial, as a delete/re-enroll would: the
	// poller must refuse the old cert during the handshake.
	stale := "deadbeef"
	_, err = st.Pool.Exec(ctx, "UPDATE nodes SET cert_serial = $1 WHERE id = $2", stale, identity.NodeID)
	require.NoError(t, err)
	node, err := st.GetNodeByID(ctx, identity.NodeID)
	require.NoError(t, err)

	conns, err := NewConnPool(ca)
	require.NoError(t, err)
	t.Cleanup(conns.Close)
	poller, err := NewPoller(st, conns, self.ID, warmSampler(t), nil, time.Hour, time.Hour)
	require.NoError(t, err)
	err = poller.heartbeat(ctx, node)
	require.Error(t, err)
	require.Contains(t, err.Error(), "serial")
}

// TestConnPoolReusesAndRedials pins the pool's caching contract: same
// node → same conn; changed advertise addr or cert serial → fresh dial.
func TestConnPoolReusesAndRedials(t *testing.T) {
	ctx := context.Background()
	st, ca, masterAddr := startMaster(t)

	token, _, err := MintJoinToken(ctx, st, ca, []string{"worker"}, nil)
	require.NoError(t, err)
	identity, _, err := RunEnroll(ctx, enrollOptions(t, masterAddr, token))
	require.NoError(t, err)
	node, err := st.GetNodeByID(ctx, identity.NodeID)
	require.NoError(t, err)

	conns, err := NewConnPool(ca)
	require.NoError(t, err)
	t.Cleanup(conns.Close)

	c1, err := conns.Get(node)
	require.NoError(t, err)
	c2, err := conns.Get(node)
	require.NoError(t, err)
	require.Same(t, c1, c2, "unchanged node reuses the cached conn")

	moved := node
	moved.AdvertiseAddr = "127.0.0.1:1"
	c3, err := conns.Get(moved)
	require.NoError(t, err)
	require.NotSame(t, c1, c3, "changed addr re-dials")

	reenrolled := node
	serial := "deadbeef"
	reenrolled.CertSerial = &serial
	c4, err := conns.Get(reenrolled)
	require.NoError(t, err)
	require.NotSame(t, c3, c4, "changed serial re-dials")

	conns.Retain(map[uuid.UUID]struct{}{})
	c5, err := conns.Get(node)
	require.NoError(t, err)
	require.NotSame(t, c4, c5, "retained-away nodes dial fresh")
}

// TestPollerRecordsContainers drives the full observed-state path: worker
// reports containers via heartbeat, the master upserts rows, a removed
// container flips to gone, and an unknown report (engine down) leaves rows
// untouched.
func TestPollerRecordsContainers(t *testing.T) {
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

	fake := enginetest.New("nginx:alpine")
	cid, err := fake.Create(ctx, engine.ContainerSpec{
		Name: "web", Image: "nginx:alpine",
		Labels: map[string]string{engine.LabelKind: engine.KindApplication},
	})
	require.NoError(t, err)
	require.NoError(t, fake.Start(ctx, cid))

	containers := engine.NewSampler(fake)
	containers.SampleNow(ctx) // first sample: state known, stats pending
	containers.SampleNow(ctx) // second: stats derived
	agent := NewAgentServer(identity, warmSampler(t), fake, containers, nil)
	go agent.Serve(lis) //nolint:errcheck
	t.Cleanup(agent.Stop)

	conns, err := NewConnPool(ca)
	require.NoError(t, err)
	t.Cleanup(conns.Close)
	poller, err := NewPoller(st, conns, self.ID, warmSampler(t), nil, 25*time.Millisecond, 100*time.Millisecond)
	require.NoError(t, err)
	pollCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go poller.Run(pollCtx)

	getRow := func() (store.NodeContainer, error) {
		return st.GetNodeContainer(ctx, store.GetNodeContainerParams{
			NodeID: identity.NodeID, ContainerID: cid,
		})
	}

	// The row lands with kind, state, labels, and stats.
	require.Eventually(t, func() bool {
		row, err := getRow()
		return err == nil && row.State == "running" && row.CpuPct != nil
	}, 5*time.Second, 20*time.Millisecond)
	row, err := getRow()
	require.NoError(t, err)
	require.Equal(t, "web", row.Name)
	require.Equal(t, engine.KindApplication, row.Kind)
	var labels map[string]string
	require.NoError(t, json.Unmarshal(row.Labels, &labels))
	require.Equal(t, "true", labels[engine.LabelManaged])

	// Engine unreachable: the report goes absent; rows must stay put.
	fake.ListErr = context.DeadlineExceeded
	containers.SampleNow(ctx)
	time.Sleep(150 * time.Millisecond) // several ticks
	row, err = getRow()
	require.NoError(t, err)
	require.Equal(t, "running", row.State, "unknown report must not touch rows")

	// Engine back, container removed behind skali's back: flips to gone.
	fake.ListErr = nil
	require.NoError(t, fake.Remove(ctx, cid, true))
	containers.SampleNow(ctx)
	require.Eventually(t, func() bool {
		row, err := getRow()
		return err == nil && row.State == "gone"
	}, 5*time.Second, 20*time.Millisecond)
}

