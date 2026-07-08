package reconcile

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/cluster"
	"github.com/Hinkolas/skali/internal/engine"
	"github.com/Hinkolas/skali/internal/engine/enginetest"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/testdb"
)

// TestConvergeAgainstRealAgent runs one convergence over the real wire: a
// worker enrolled through the actual mTLS enrollment flow, its NodeService
// served over gRPC, and the reconciler driving it through remote handles —
// the same path production takes for every non-master node.
func TestConvergeAgainstRealAgent(t *testing.T) {
	ctx := context.Background()
	st := store.NewStore(testdb.New(t))
	ca, err := cluster.EnsureCA(ctx, st, strings.Repeat("s", 32))
	require.NoError(t, err)

	// Master enrollment listener.
	master, err := cluster.NewMasterServer(st, ca, nil, "")
	require.NoError(t, err)
	masterLis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go master.Serve(masterLis) //nolint:errcheck
	t.Cleanup(master.Stop)

	// Enroll a worker and serve its NodeService over the issued identity.
	token, _, err := cluster.MintJoinToken(ctx, st, ca, []string{"worker"}, nil)
	require.NoError(t, err)
	agentLis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	identity, _, err := cluster.RunEnroll(ctx, cluster.EnrollOptions{
		MasterAddr:    masterLis.Addr().String(),
		Token:         token,
		AdvertiseAddr: agentLis.Addr().String(),
		GRPCAddr:      agentLis.Addr().String(),
		DataDir:       t.TempDir(),
		CertsDir:      t.TempDir(),
	})
	require.NoError(t, err)
	fake := enginetest.New()
	agent := cluster.NewAgentServer(identity, nil, fake, nil, nil, nil)
	go agent.Serve(agentLis) //nolint:errcheck
	t.Cleanup(agent.Stop)

	nodes, err := st.ListNodes(ctx)
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	_, err = st.Pool.Exec(ctx, "UPDATE nodes SET status = 'online' WHERE id = $1", nodes[0].ID)
	require.NoError(t, err)

	conns, err := cluster.NewConnPool(ca)
	require.NoError(t, err)
	t.Cleanup(conns.Close)

	imp := &stubImporter{st: st, salt: "v1"}
	rec := NewReconciler(st, cluster.NewNodeHandles(conns, uuid.Nil, nil), imp, "registry.local:5000")
	svc := NewService(st, nil)
	w, err := svc.Create(ctx, WorkloadInput{
		Name: "wire", Kind: engine.KindApplication, Image: "alpine:3",
		Replicas: 1, DesiredState: DesiredRunning,
	})
	require.NoError(t, err)

	rec.pass(ctx)
	require.Eventually(t, rec.idle, 15*time.Second, 5*time.Millisecond)

	ctrs, err := fake.List(ctx)
	require.NoError(t, err)
	require.Len(t, ctrs, 1)
	require.Equal(t, "running", ctrs[0].State)
	require.Equal(t, "wire-0", ctrs[0].Name)
	require.Equal(t, w.ID.String(), ctrs[0].Labels[engine.LabelWorkload])

	asgs, err := st.ListAssignmentsForWorkload(ctx, w.ID)
	require.NoError(t, err)
	require.Len(t, asgs, 1)
	require.Equal(t, PhaseReady, asgs[0].Phase)
	require.Equal(t, nodes[0].ID, *asgs[0].NodeID)
}
