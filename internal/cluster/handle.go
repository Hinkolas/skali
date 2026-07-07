package cluster

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Hinkolas/skali/internal/clusterpb"
	"github.com/Hinkolas/skali/internal/engine"
	"github.com/Hinkolas/skali/internal/store"
)

// NodeHandle is the master's imperative container surface for ONE node —
// the Executor seam the higher layers (applications, database pools, system
// components) drive. Where a container physically runs is invisible above
// this interface: the master's own node short-circuits to its local engine,
// every other node goes over the pooled mTLS connection. Engine types are
// the currency; proto exists only on the wire.
type NodeHandle interface {
	CreateContainer(ctx context.Context, spec engine.ContainerSpec, pull engine.PullPolicy, start bool) (engine.Container, error)
	StartContainer(ctx context.Context, containerID string) (engine.Container, error)
	StopContainer(ctx context.Context, containerID string, timeout time.Duration) (engine.Container, error)
	RemoveContainer(ctx context.Context, containerID string, force bool) error
}

// lifecycleTimeout bounds start/stop/remove round trips (stop adds its own
// grace period on top). Creates run longer — image pulls — and carry their
// caller's deadline instead.
const lifecycleTimeout = 30 * time.Second

// localHandle is the master's own node: no gRPC, mirroring how the poller
// stamps the self row locally.
type localHandle struct {
	eng engine.Engine
}

func (h *localHandle) CreateContainer(ctx context.Context, spec engine.ContainerSpec, pull engine.PullPolicy, start bool) (engine.Container, error) {
	c, err := engine.Deploy(ctx, h.eng, spec, pull, start)
	return c, containerErrFromEngine(err)
}

func (h *localHandle) StartContainer(ctx context.Context, id string) (engine.Container, error) {
	ctx, cancel := context.WithTimeout(ctx, lifecycleTimeout)
	defer cancel()
	if err := h.eng.Start(ctx, id); err != nil {
		return engine.Container{}, containerErrFromEngine(err)
	}
	c, err := h.eng.Inspect(ctx, id)
	return c, containerErrFromEngine(err)
}

func (h *localHandle) StopContainer(ctx context.Context, id string, timeout time.Duration) (engine.Container, error) {
	ctx, cancel := context.WithTimeout(ctx, lifecycleTimeout+timeout)
	defer cancel()
	if err := h.eng.Stop(ctx, id, timeout); err != nil {
		return engine.Container{}, containerErrFromEngine(err)
	}
	c, err := h.eng.Inspect(ctx, id)
	return c, containerErrFromEngine(err)
}

func (h *localHandle) RemoveContainer(ctx context.Context, id string, force bool) error {
	ctx, cancel := context.WithTimeout(ctx, lifecycleTimeout)
	defer cancel()
	return containerErrFromEngine(h.eng.Remove(ctx, id, force))
}

// remoteHandle drives a worker's NodeService over the shared pool.
type remoteHandle struct {
	conns *ConnPool
	node  store.Node
}

func (h *remoteHandle) client() (clusterpb.NodeServiceClient, error) {
	conn, err := h.conns.Get(h.node)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNodeUnreachable, err)
	}
	return clusterpb.NewNodeServiceClient(conn), nil
}

func (h *remoteHandle) CreateContainer(ctx context.Context, spec engine.ContainerSpec, pull engine.PullPolicy, start bool) (engine.Container, error) {
	client, err := h.client()
	if err != nil {
		return engine.Container{}, err
	}
	resp, err := client.CreateContainer(ctx, &clusterpb.CreateContainerRequest{
		Spec:       specToProto(spec),
		PullPolicy: pullToProto(pull),
		Start:      start,
	})
	if err != nil {
		return engine.Container{}, containerErrFromRPC(err)
	}
	return containerFromProto(resp.GetContainer()), nil
}

func (h *remoteHandle) StartContainer(ctx context.Context, id string) (engine.Container, error) {
	client, err := h.client()
	if err != nil {
		return engine.Container{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, lifecycleTimeout)
	defer cancel()
	resp, err := client.StartContainer(ctx, &clusterpb.StartContainerRequest{ContainerId: id})
	if err != nil {
		return engine.Container{}, containerErrFromRPC(err)
	}
	return containerFromProto(resp.GetContainer()), nil
}

func (h *remoteHandle) StopContainer(ctx context.Context, id string, timeout time.Duration) (engine.Container, error) {
	client, err := h.client()
	if err != nil {
		return engine.Container{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, lifecycleTimeout+timeout)
	defer cancel()
	resp, err := client.StopContainer(ctx, &clusterpb.StopContainerRequest{
		ContainerId:    id,
		TimeoutSeconds: uint32(timeout / time.Second),
	})
	if err != nil {
		return engine.Container{}, containerErrFromRPC(err)
	}
	return containerFromProto(resp.GetContainer()), nil
}

func (h *remoteHandle) RemoveContainer(ctx context.Context, id string, force bool) error {
	client, err := h.client()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, lifecycleTimeout)
	defer cancel()
	_, err = client.RemoveContainer(ctx, &clusterpb.RemoveContainerRequest{ContainerId: id, Force: force})
	return containerErrFromRPC(err)
}

// containerErrFromEngine maps engine sentinels (the local path) onto the
// cluster sentinels the REST layer understands.
func containerErrFromEngine(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, engine.ErrNotFound):
		return fmt.Errorf("%w: %v", ErrContainerNotFound, err)
	case errors.Is(err, engine.ErrConflict):
		return fmt.Errorf("%w: %v", ErrContainerConflict, err)
	case errors.Is(err, engine.ErrImageMissing), errors.Is(err, engine.ErrNotManaged):
		return fmt.Errorf("%w: %v", ErrInvalidSpec, err)
	case errors.Is(err, engine.ErrEngineUnavailable):
		return fmt.Errorf("%w: %v", ErrEngineUnavailable, err)
	default:
		return err
	}
}

// containerErrFromRPC is the inverse of the node's grpcEngineErr: gRPC
// status codes back to cluster sentinels.
func containerErrFromRPC(err error) error {
	if err == nil {
		return nil
	}
	st, ok := status.FromError(err)
	if !ok {
		return err
	}
	switch st.Code() {
	case codes.NotFound:
		return fmt.Errorf("%w: %s", ErrContainerNotFound, st.Message())
	case codes.AlreadyExists:
		return fmt.Errorf("%w: %s", ErrContainerConflict, st.Message())
	case codes.InvalidArgument, codes.FailedPrecondition:
		return fmt.Errorf("%w: %s", ErrInvalidSpec, st.Message())
	case codes.Unavailable:
		// Server-reported Unavailable is the node's engine being down; the
		// same code from the transport layer means the node itself is.
		if strings.Contains(st.Message(), "engine") {
			return fmt.Errorf("%w: %s", ErrEngineUnavailable, st.Message())
		}
		return fmt.Errorf("%w: %s", ErrNodeUnreachable, st.Message())
	case codes.DeadlineExceeded:
		return fmt.Errorf("%w: %s", ErrNodeUnreachable, st.Message())
	default:
		return err
	}
}
