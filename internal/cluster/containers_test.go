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
	agent := NewAgentServer(identity, warmSampler(t), fake, containers, nil, nil)
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
	_, err = ops.Start(ctx, [16]byte{1}, "whatever")
	require.ErrorIs(t, err, ErrNodeNotFound)
}

// TestImageOpsRemoteEndToEnd drives the image primitives against a real
// fake-engine agent over mTLS: pull with immediate row write-through, remove
// with precise row deletion, and the sentinel mapping.
func TestImageOpsRemoteEndToEnd(t *testing.T) {
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
	_, containers := testContainerDeps(t)
	agent := NewAgentServer(identity, warmSampler(t), fake, containers, engine.NewInventorySampler(fake), nil)
	go agent.Serve(lis) //nolint:errcheck
	t.Cleanup(agent.Stop)

	conns, err := NewConnPool(ca)
	require.NoError(t, err)
	t.Cleanup(conns.Close)
	ops := NewContainerOps(st, conns, self.ID, enginetest.New())

	// Pull: the row is written through immediately, before any heartbeat.
	row, err := ops.PullImage(ctx, identity.NodeID, "redis:7")
	require.NoError(t, err)
	require.Equal(t, "sha256:fake-redis:7", row.ImageID)
	require.Equal(t, []string{"redis:7"}, row.RepoTags)
	require.False(t, row.Dangling)

	// Remove: the node reports what it deleted; exactly those rows go.
	require.NoError(t, ops.RemoveImage(ctx, identity.NodeID, "redis:7", false))
	_, err = st.GetNodeImage(ctx, store.GetNodeImageParams{NodeID: identity.NodeID, ImageID: row.ImageID})
	require.Error(t, err, "row deleted with the image")

	// Sentinels: unknown ref, in-use without force, empty ref, unknown node.
	require.ErrorIs(t, ops.RemoveImage(ctx, identity.NodeID, "ghost:1", false), ErrImageNotFound)
	fake.Inv.Images = append(fake.Inv.Images, engine.Image{
		ID: "sha256:busy", RepoTags: []string{"busy:1"}, Containers: 1,
	})
	require.ErrorIs(t, ops.RemoveImage(ctx, identity.NodeID, "busy:1", false), ErrImageInUse)
	_, err = ops.PullImage(ctx, identity.NodeID, "")
	require.ErrorIs(t, err, ErrInvalidRef)
	_, err = ops.PullImage(ctx, [16]byte{1}, "redis:7")
	require.ErrorIs(t, err, ErrNodeNotFound)
}
