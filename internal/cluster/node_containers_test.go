package cluster

import (
	"context"
	"crypto/tls"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	"github.com/Hinkolas/skali/internal/clusterpb"
	"github.com/Hinkolas/skali/internal/engine"
	"github.com/Hinkolas/skali/internal/engine/enginetest"
)

// testContainerDeps returns a fake engine plus a warmed container sampler
// over it — the container-side analog of warmSampler for agent servers.
func testContainerDeps(t *testing.T) (*enginetest.Fake, *engine.Sampler) {
	t.Helper()
	fake := enginetest.New()
	s := engine.NewSampler(fake)
	s.SampleNow(context.Background())
	return fake, s
}

// startAgentWithEngine enrolls a worker, serves its NodeService over a fake
// engine, and returns a master-authenticated client for it.
func startAgentWithEngine(t *testing.T, fake *enginetest.Fake) (clusterpb.NodeServiceClient, *engine.Sampler, *engine.InventorySampler) {
	t.Helper()
	ctx := context.Background()
	st, ca, masterAddr := startMaster(t)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	token, _, err := MintJoinToken(ctx, st, ca, []string{"worker"}, nil)
	require.NoError(t, err)
	opts := enrollOptions(t, masterAddr, token)
	opts.AdvertiseAddr = lis.Addr().String()
	identity, _, err := RunEnroll(ctx, opts)
	require.NoError(t, err)

	containers := engine.NewSampler(fake)
	containers.SampleNow(ctx)
	inventory := engine.NewInventorySampler(fake)
	inventory.SampleNow(ctx)
	agent := NewAgentServer(identity, warmSampler(t), fake, containers, inventory, nil)
	go agent.Serve(lis) //nolint:errcheck
	t.Cleanup(agent.Stop)

	clientCert, err := ca.IssueClientCert()
	require.NoError(t, err)
	conn, err := grpc.NewClient(lis.Addr().String(),
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
			MinVersion:   tls.VersionTLS13,
			Certificates: []tls.Certificate{clientCert},
			RootCAs:      ca.Pool(),
			ServerName:   identity.NodeID.String(),
		})))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return clusterpb.NewNodeServiceClient(conn), containers, inventory
}

func TestNodeServiceContainerLifecycle(t *testing.T) {
	ctx := context.Background()
	fake := enginetest.New("nginx:alpine")
	client, containers, _ := startAgentWithEngine(t, fake)

	// Create + start in one round trip.
	created, err := client.CreateContainer(ctx, &clusterpb.CreateContainerRequest{
		Spec: specToProto(engine.ContainerSpec{
			Name:   "web",
			Image:  "nginx:alpine",
			Labels: map[string]string{engine.LabelKind: engine.KindApplication},
		}),
		Start: true,
	})
	require.NoError(t, err)
	c := created.GetContainer()
	require.Equal(t, "web", c.GetName())
	require.Equal(t, "running", c.GetState())
	require.Equal(t, "true", c.GetLabels()[engine.LabelManaged])
	require.Equal(t, engine.KindApplication, c.GetLabels()[engine.LabelKind])

	// The heartbeat report carries it once the sampler has seen it.
	containers.SampleNow(ctx)
	hb, err := client.Heartbeat(ctx, &clusterpb.HeartbeatRequest{})
	require.NoError(t, err)
	require.NotNil(t, hb.GetContainers())
	require.Len(t, hb.GetContainers().GetContainers(), 1)
	require.Equal(t, c.GetId(), hb.GetContainers().GetContainers()[0].GetId())

	// Stop and remove; post-op state comes back on the wire.
	stopped, err := client.StopContainer(ctx, &clusterpb.StopContainerRequest{ContainerId: c.GetId()})
	require.NoError(t, err)
	require.Equal(t, "exited", stopped.GetContainer().GetState())

	_, err = client.RemoveContainer(ctx, &clusterpb.RemoveContainerRequest{ContainerId: c.GetId()})
	require.NoError(t, err)
	_, ok := fake.Get(c.GetId())
	require.False(t, ok)
}

func TestNodeServiceErrorMapping(t *testing.T) {
	ctx := context.Background()
	fake := enginetest.New("nginx:alpine")
	client, _, _ := startAgentWithEngine(t, fake)

	// Missing kind label: rejected before the engine is touched.
	_, err := client.CreateContainer(ctx, &clusterpb.CreateContainerRequest{
		Spec: specToProto(engine.ContainerSpec{Name: "x", Image: "nginx:alpine"}),
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	// Unknown container id.
	_, err = client.StartContainer(ctx, &clusterpb.StartContainerRequest{ContainerId: "nope"})
	require.Equal(t, codes.NotFound, status.Code(err))

	// Name conflict.
	spec := specToProto(engine.ContainerSpec{
		Name: "dup", Image: "nginx:alpine",
		Labels: map[string]string{engine.LabelKind: engine.KindSystem},
	})
	_, err = client.CreateContainer(ctx, &clusterpb.CreateContainerRequest{Spec: spec})
	require.NoError(t, err)
	_, err = client.CreateContainer(ctx, &clusterpb.CreateContainerRequest{Spec: spec})
	require.Equal(t, codes.AlreadyExists, status.Code(err))

	// Missing image under pull-never.
	_, err = client.CreateContainer(ctx, &clusterpb.CreateContainerRequest{
		Spec: specToProto(engine.ContainerSpec{
			Name: "noimg", Image: "ghost:latest",
			Labels: map[string]string{engine.LabelKind: engine.KindSystem},
		}),
		PullPolicy: clusterpb.PullPolicy_PULL_POLICY_NEVER,
	})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
}

func TestHeartbeatContainerReportAbsentWhenUnknown(t *testing.T) {
	ctx := context.Background()
	fake := enginetest.New("nginx:alpine")
	client, containers, _ := startAgentWithEngine(t, fake)

	// Engine goes unreachable: the report must vanish (unknown), not read as
	// "no containers".
	fake.ListErr = context.DeadlineExceeded
	containers.SampleNow(ctx)

	deadline, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	hb, err := client.Heartbeat(deadline, &clusterpb.HeartbeatRequest{})
	require.NoError(t, err)
	require.Nil(t, hb.GetContainers())
	require.NotNil(t, hb.GetMetrics(), "host metrics are independent of the engine")
}

func TestHeartbeatInventoryReport(t *testing.T) {
	ctx := context.Background()
	fake := enginetest.New("nginx:alpine")
	fake.Inv = engine.Inventory{
		Images:  []engine.Image{{ID: "sha256:abc", RepoTags: []string{"nginx:alpine"}, SizeBytes: 42, Containers: 1}},
		Volumes: []engine.Volume{{Name: "data", Driver: "local", Containers: 1}},
	}
	client, _, inventory := startAgentWithEngine(t, fake)

	// The warmed sampler's snapshot rides the heartbeat.
	hb, err := client.Heartbeat(ctx, &clusterpb.HeartbeatRequest{})
	require.NoError(t, err)
	inv := hb.GetInventory()
	require.NotNil(t, inv)
	require.Len(t, inv.GetImages(), 1)
	require.Equal(t, "sha256:abc", inv.GetImages()[0].GetId())
	require.Equal(t, uint32(1), inv.GetImages()[0].GetContainers())
	require.Len(t, inv.GetVolumes(), 1)
	require.Equal(t, "data", inv.GetVolumes()[0].GetName())

	// Engine unreachable: the report must vanish (unknown), never read as
	// "empty node".
	fake.InventoryErr = context.DeadlineExceeded
	inventory.SampleNow(ctx)
	hb, err = client.Heartbeat(ctx, &clusterpb.HeartbeatRequest{})
	require.NoError(t, err)
	require.Nil(t, hb.GetInventory())
}

func TestNodeServiceImagePrimitives(t *testing.T) {
	ctx := context.Background()
	fake := enginetest.New()
	client, _, _ := startAgentWithEngine(t, fake)

	// Pull: post-op state comes back, and the heartbeat inventory already
	// contains it — the node re-samples synchronously after the mutation.
	pulled, err := client.PullImage(ctx, &clusterpb.PullImageRequest{Reference: "redis:7"})
	require.NoError(t, err)
	require.Equal(t, "sha256:fake-redis:7", pulled.GetImage().GetId())
	require.Equal(t, []string{"redis:7"}, pulled.GetImage().GetRepoTags())
	hb, err := client.Heartbeat(ctx, &clusterpb.HeartbeatRequest{})
	require.NoError(t, err)
	require.Len(t, hb.GetInventory().GetImages(), 1)

	// Remove: deleted ids come back and the report is consistent again.
	removed, err := client.RemoveImage(ctx, &clusterpb.RemoveImageRequest{Reference: "redis:7"})
	require.NoError(t, err)
	require.Equal(t, []string{"sha256:fake-redis:7"}, removed.GetDeletedIds())
	hb, err = client.Heartbeat(ctx, &clusterpb.HeartbeatRequest{})
	require.NoError(t, err)
	require.Empty(t, hb.GetInventory().GetImages())

	// Error mapping: unknown → NotFound; in use without force → AlreadyExists.
	_, err = client.RemoveImage(ctx, &clusterpb.RemoveImageRequest{Reference: "ghost:1"})
	require.Equal(t, codes.NotFound, status.Code(err))
	fake.Inv.Images = append(fake.Inv.Images, engine.Image{
		ID: "sha256:busy", RepoTags: []string{"busy:1"}, Containers: 1,
	})
	_, err = client.RemoveImage(ctx, &clusterpb.RemoveImageRequest{Reference: "busy:1"})
	require.Equal(t, codes.AlreadyExists, status.Code(err))
}
