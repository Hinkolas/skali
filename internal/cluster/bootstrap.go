package cluster

import (
	"context"
	"errors"
	"runtime"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/version"
)

// EnsureSelfNode creates the master's own node row on first boot. The master
// is a worker too (single-node clusters run everything); the edge role stays
// an operator decision in the UI.
func EnsureSelfNode(ctx context.Context, st *store.Store, clusterAddr string) (store.Node, error) {
	node, err := st.GetMasterNode(ctx)
	if err == nil {
		return node, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return store.Node{}, err
	}

	id, err := uuid.NewV7()
	if err != nil {
		return store.Node{}, err
	}
	arch, os_, ver := runtime.GOARCH, runtime.GOOS, version.Version
	return st.CreateNode(ctx, store.CreateNodeParams{
		ID:            id,
		Name:          hostnameOrDefault(),
		Roles:         []string{"master", "worker"},
		AdvertiseAddr: clusterAddr,
		Arch:          &arch,
		Os:            &os_,
		SkalidVersion: &ver,
	})
}
