package cluster

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Hinkolas/skali/internal/store"
)

// Service is the node-management surface consumed by the REST layer. Business
// rules live here; handlers only translate HTTP.
type Service struct {
	st          *store.Store
	ca          *CA
	clusterAddr string
}

func NewService(st *store.Store, ca *CA, clusterAddr string) *Service {
	return &Service{st: st, ca: ca, clusterAddr: clusterAddr}
}

// JoinTokenResult is what the operator needs to add a node: the raw token and
// the ready-to-paste enroll command.
type JoinTokenResult struct {
	Token         string
	ExpiresAt     time.Time
	EnrollCommand string
}

// CreateJoinToken mints a one-time enrollment token and renders the enroll
// command against CLUSTER_ADDR. Fails with ErrClusterAddrUnset when the
// master has no externally reachable address configured.
func (s *Service) CreateJoinToken(ctx context.Context, roles []string, createdBy uuid.UUID) (JoinTokenResult, error) {
	if s.clusterAddr == "" {
		return JoinTokenResult{}, ErrClusterAddrUnset
	}
	token, row, err := MintJoinToken(ctx, s.st, s.ca, roles, &createdBy)
	if err != nil {
		return JoinTokenResult{}, err
	}
	return JoinTokenResult{
		Token:         token,
		ExpiresAt:     row.ExpiresAt,
		EnrollCommand: fmt.Sprintf("skalid enroll --master %s --token %s", s.clusterAddr, token),
	}, nil
}

// NodeUpdate carries the PATCHable node fields; nil = leave unchanged. An
// empty PublicAddr clears the column.
type NodeUpdate struct {
	Name       *string
	Roles      []string
	PublicAddr *string
}

// UpdateNode applies a partial update. The master role is fixed: it can be
// neither granted nor removed (ErrMasterNode).
func (s *Service) UpdateNode(ctx context.Context, id uuid.UUID, upd NodeUpdate) (store.Node, error) {
	var node store.Node
	err := s.st.WithTx(ctx, func(q *store.Queries) error {
		var err error
		node, err = q.GetNodeByID(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNodeNotFound
		}
		if err != nil {
			return err
		}
		if upd.Name != nil && *upd.Name != "" {
			node, err = q.SetNodeName(ctx, store.SetNodeNameParams{ID: id, Name: *upd.Name})
			if err != nil {
				return err
			}
		}
		if upd.Roles != nil {
			if err := ValidateRoles(upd.Roles, AllRoles); err != nil {
				return err
			}
			if slices.Contains(node.Roles, "master") != slices.Contains(upd.Roles, "master") {
				return ErrMasterNode
			}
			node, err = q.SetNodeRoles(ctx, store.SetNodeRolesParams{ID: id, Roles: upd.Roles})
			if err != nil {
				return err
			}
		}
		if upd.PublicAddr != nil {
			addr := upd.PublicAddr
			if *addr == "" {
				addr = nil // clear
			}
			node, err = q.SetNodePublicAddr(ctx, store.SetNodePublicAddrParams{ID: id, PublicAddr: addr})
			if err != nil {
				return err
			}
		}
		return nil
	})
	return node, err
}

// DeleteNode removes a node from the cluster. The master's own row is
// protected (ErrMasterNode). The node's cert is implicitly revoked: its
// serial no longer matches any row, so the poller stops dialing it and the
// master refuses it everywhere serials are checked.
func (s *Service) DeleteNode(ctx context.Context, id uuid.UUID) error {
	return s.st.WithTx(ctx, func(q *store.Queries) error {
		node, err := q.GetNodeByID(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNodeNotFound
		}
		if err != nil {
			return err
		}
		if slices.Contains(node.Roles, "master") {
			return ErrMasterNode
		}
		_, err = q.DeleteNodeByID(ctx, id)
		return err
	})
}
