package cluster

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/engine"
	"github.com/Hinkolas/skali/internal/engine/enginetest"
	"github.com/Hinkolas/skali/internal/store"
)

// TestContainerOpsRemoteEndToEnd drives ContainerOps against a real
// fake-engine agent over mTLS: the remote-handle path, spec/label stamping,
// and the immediate observed-state write-through.
func TestContainerOpsRemoteEndToEnd(t *testing.T) {
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
	containers := engine.NewSampler(fake)
	containers.SampleNow(ctx)
	agent := NewAgentServer(identity, warmSampler(t), fake, containers)
	go agent.Serve(lis) //nolint:errcheck
	t.Cleanup(agent.Stop)

	conns, err := NewConnPool(ca)
	require.NoError(t, err)
	t.Cleanup(conns.Close)
	ops := NewContainerOps(st, conns, self.ID, enginetest.New())

	// Create on the worker: user labels pass through, identity is stamped.
	row, err := ops.Create(ctx, identity.NodeID, CreateContainerInput{
		Spec: engine.ContainerSpec{
			Name:   "web",
			Image:  "nginx:alpine",
			Labels: map[string]string{"team": "storefront"},
		},
		Kind:  engine.KindApplication,
		Start: true,
	})
	require.NoError(t, err)
	require.Equal(t, "running", row.State)
	require.Equal(t, engine.KindApplication, row.Kind)
	ctr, ok := fake.Get(row.ContainerID)
	require.True(t, ok, "container exists on the worker's engine")
	require.Equal(t, "true", ctr.Labels[engine.LabelManaged])
	require.Equal(t, "storefront", ctr.Labels["team"])

	// Stop and start round-trip fresh state into observed rows.
	row, err = ops.Stop(ctx, identity.NodeID, row.ContainerID, 2*time.Second)
	require.NoError(t, err)
	require.Equal(t, "exited", row.State)
	row, err = ops.Start(ctx, identity.NodeID, row.ContainerID)
	require.NoError(t, err)
	require.Equal(t, "running", row.State)

	// Unknown ids surface the sentinel; Remove stays idempotent.
	_, err = ops.Start(ctx, identity.NodeID, "nope")
	require.ErrorIs(t, err, ErrContainerNotFound)
	require.NoError(t, ops.Remove(ctx, identity.NodeID, "nope", false))

	require.NoError(t, ops.Remove(ctx, identity.NodeID, row.ContainerID, true))
	_, ok = fake.Get(row.ContainerID)
	require.False(t, ok)
	_, err = st.GetNodeContainer(ctx, store.GetNodeContainerParams{
		NodeID: identity.NodeID, ContainerID: row.ContainerID,
	})
	require.Error(t, err, "observed row deleted with the container")

	// Unknown node.
	_, err = ops.List(ctx, [16]byte{1})
	require.ErrorIs(t, err, ErrNodeNotFound)
}
