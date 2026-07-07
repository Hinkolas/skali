package cluster

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Hinkolas/skali/internal/engine"
	"github.com/Hinkolas/skali/internal/store"
)

// Container sentinels, mapped by the REST layer.
var (
	ErrContainerNotFound = errors.New("cluster: container not found")
	ErrContainerConflict = errors.New("cluster: container name already in use")
	ErrInvalidSpec       = errors.New("cluster: invalid container spec")
	ErrNodeUnreachable   = errors.New("cluster: node unreachable")
	ErrEngineUnavailable = errors.New("cluster: container engine unavailable on node")
)

// containerNameRe mirrors the engine's container name rules.
var containerNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

// ContainerOps is the master-side container service: validation, label
// stamping, dispatch to the owning node's handle, and the immediate
// observed-state write-through. The REST layer is a thin shell over it; the
// application/database layers later build on the same NodeHandles.
//
// Mutations upsert/delete the node_containers row on success so the UI
// reflects actions instantly; the heartbeat remains the reconciler of
// record (e.g. a start/stop of a container the node no longer knows returns
// ErrContainerNotFound here and the next report retires the row).
type ContainerOps struct {
	st        *store.Store
	handleFor func(store.Node) NodeHandle // seam: tests inject fakes
}

// NewContainerOps wires the ops service. eng is the master's own engine
// (the local handle); every other node is driven over conns.
func NewContainerOps(st *store.Store, conns *ConnPool, selfID uuid.UUID, eng engine.Engine) *ContainerOps {
	return &ContainerOps{
		st: st,
		handleFor: func(n store.Node) NodeHandle {
			if n.ID == selfID {
				return &localHandle{eng: eng}
			}
			return &remoteHandle{conns: conns, node: n}
		},
	}
}

// CreateContainerInput is the admin create surface. Kind is explicit and
// required — there is no default; Labels are user labels only (the skali.*
// namespace is stamped here, never passed through).
type CreateContainerInput struct {
	Spec  engine.ContainerSpec
	Kind  string
	Pull  engine.PullPolicy
	Start bool
}

// Create validates, stamps ownership + kind, deploys on the owning node,
// and records the result.
func (c *ContainerOps) Create(ctx context.Context, nodeID uuid.UUID, in CreateContainerInput) (store.NodeContainer, error) {
	node, err := c.node(ctx, nodeID)
	if err != nil {
		return store.NodeContainer{}, err
	}
	if err := c.validate(in); err != nil {
		return store.NodeContainer{}, err
	}

	spec := in.Spec
	labels := make(map[string]string, len(spec.Labels)+2)
	maps.Copy(labels, spec.Labels)
	labels[engine.LabelManaged] = "true"
	labels[engine.LabelKind] = in.Kind
	spec.Labels = labels

	ctr, err := c.handleFor(node).CreateContainer(ctx, spec, in.Pull, in.Start)
	if err != nil {
		return store.NodeContainer{}, err
	}
	return c.record(ctx, nodeID, ctr)
}

func (c *ContainerOps) validate(in CreateContainerInput) error {
	if err := engine.ValidateKind(in.Kind); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidSpec, err)
	}
	if err := engine.ValidateUserLabels(in.Spec.Labels); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidSpec, err)
	}
	if !containerNameRe.MatchString(in.Spec.Name) {
		return fmt.Errorf("%w: name must match %s", ErrInvalidSpec, containerNameRe)
	}
	if in.Spec.Image == "" {
		return fmt.Errorf("%w: image is required", ErrInvalidSpec)
	}
	switch in.Pull {
	case "", engine.PullIfMissing, engine.PullAlways, engine.PullNever:
	default:
		return fmt.Errorf("%w: invalid pull policy %q", ErrInvalidSpec, in.Pull)
	}
	return nil
}

// Start starts a known container on its node and records the fresh state.
func (c *ContainerOps) Start(ctx context.Context, nodeID uuid.UUID, containerID string) (store.NodeContainer, error) {
	node, err := c.node(ctx, nodeID)
	if err != nil {
		return store.NodeContainer{}, err
	}
	ctr, err := c.handleFor(node).StartContainer(ctx, containerID)
	if err != nil {
		return store.NodeContainer{}, err
	}
	return c.record(ctx, nodeID, ctr)
}

// Stop gracefully stops a container; timeout <= 0 uses the engine default.
func (c *ContainerOps) Stop(ctx context.Context, nodeID uuid.UUID, containerID string, timeout time.Duration) (store.NodeContainer, error) {
	node, err := c.node(ctx, nodeID)
	if err != nil {
		return store.NodeContainer{}, err
	}
	ctr, err := c.handleFor(node).StopContainer(ctx, containerID, timeout)
	if err != nil {
		return store.NodeContainer{}, err
	}
	return c.record(ctx, nodeID, ctr)
}

// Remove removes a container. Idempotent by design — the admin escape hatch
// must work on a container the node already lost — so node-side NotFound is
// success; the observed row is deleted either way.
func (c *ContainerOps) Remove(ctx context.Context, nodeID uuid.UUID, containerID string, force bool) error {
	node, err := c.node(ctx, nodeID)
	if err != nil {
		return err
	}
	if err := c.handleFor(node).RemoveContainer(ctx, containerID, force); err != nil && !errors.Is(err, ErrContainerNotFound) {
		return err
	}
	_, err = c.st.DeleteNodeContainer(ctx, store.DeleteNodeContainerParams{
		NodeID: nodeID, ContainerID: containerID,
	})
	return err
}

// List returns the node's observed containers from the master's records —
// never a live call; heartbeats keep the records honest.
func (c *ContainerOps) List(ctx context.Context, nodeID uuid.UUID) ([]store.NodeContainer, error) {
	if _, err := c.node(ctx, nodeID); err != nil {
		return nil, err
	}
	return c.st.ListNodeContainers(ctx, nodeID)
}

func (c *ContainerOps) node(ctx context.Context, id uuid.UUID) (store.Node, error) {
	node, err := c.st.GetNodeByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Node{}, ErrNodeNotFound
	}
	return node, err
}

// record writes a mutation's post-op state through to observed state and
// returns the stored row.
func (c *ContainerOps) record(ctx context.Context, nodeID uuid.UUID, ctr engine.Container) (store.NodeContainer, error) {
	params, err := upsertNodeContainerParams(nodeID, containerInfoProto(ctr, nil))
	if err != nil {
		return store.NodeContainer{}, err
	}
	if err := c.st.UpsertNodeContainer(ctx, params); err != nil {
		return store.NodeContainer{}, err
	}
	return c.st.GetNodeContainer(ctx, store.GetNodeContainerParams{
		NodeID: nodeID, ContainerID: ctr.ID,
	})
}
