package dbstore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/store"
)

func TestEnvironmentInterceptsRoundtrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)

	insert := func(key, ports string) {
		require.NoError(t, f.st.InsertEnvironmentIntercept(ctx, store.InsertEnvironmentInterceptParams{
			EnvironmentID:  f.environmentID,
			ApplicationKey: key,
			Ports:          []byte(ports),
		}))
	}
	insert("web", `{"web":5173}`)
	insert("api", `{"http":3000}`)

	rows, err := f.st.ListEnvironmentIntercepts(ctx, f.environmentID)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, "api", rows[0].ApplicationKey)
	require.Equal(t, "web", rows[1].ApplicationKey)
	require.JSONEq(t, `{"web":5173}`, string(rows[1].Ports))

	require.NoError(t, f.st.DeleteEnvironmentIntercepts(ctx, f.environmentID))
	rows, err = f.st.ListEnvironmentIntercepts(ctx, f.environmentID)
	require.NoError(t, err)
	require.Empty(t, rows)
}

func TestAllocateClusterNodePort(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)

	first := f.cluster(t, "pg17-shared")
	port, err := f.svc.AllocateClusterNodePort(ctx, first.ID, 30501, 30502)
	require.NoError(t, err)
	require.Equal(t, 30501, port)

	// Idempotent: a second call returns the held port.
	port, err = f.svc.AllocateClusterNodePort(ctx, first.ID, 30501, 30502)
	require.NoError(t, err)
	require.Equal(t, 30501, port)

	second, err := f.svc.CreateCluster(ctx, ClusterInput{
		Name: "pg16-shared", Engine: "postgres", Major: 16,
		Class: ClassShared, Instances: 1, StorageBytes: 1 << 30, Image: "test-image:16",
	})
	require.NoError(t, err)
	port, err = f.svc.AllocateClusterNodePort(ctx, second.ID, 30501, 30502)
	require.NoError(t, err)
	require.Equal(t, 30502, port)

	third, err := f.svc.CreateCluster(ctx, ClusterInput{
		Name: "pg15-shared", Engine: "postgres", Major: 15,
		Class: ClassShared, Instances: 1, StorageBytes: 1 << 30, Image: "test-image:15",
	})
	require.NoError(t, err)
	_, err = f.svc.AllocateClusterNodePort(ctx, third.ID, 30501, 30502)
	require.ErrorIs(t, err, ErrNodePortsExhausted)

	// Releasing a pool frees its port for the next allocation.
	released, err := f.st.SetDatabaseClusterState(ctx, store.SetDatabaseClusterStateParams{
		ID: second.ID, ToState: StateReleased, FromState: StateActive,
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, released)
	port, err = f.svc.AllocateClusterNodePort(ctx, third.ID, 30501, 30502)
	require.NoError(t, err)
	require.Equal(t, 30502, port)
}
