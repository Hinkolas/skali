package substrate

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/store"
)

// TestGrowShape: the recorded topology only ever grows with the fleet;
// a smaller fleet keeps the shape and is reported as below it.
func TestGrowShape(t *testing.T) {
	t.Parallel()
	single := store.ObjectStore{Name: "s", Masters: 1, VolumeServers: 1, Replication: "000", Image: "img"}

	same, grew, below := growShape(single, desiredShape(1))
	require.False(t, grew)
	require.False(t, below)
	require.Equal(t, dbstore.StoreInput{Name: "s", Masters: 1, VolumeServers: 1, Replication: "000", Image: "img"}, same)

	// A second node: a replica and a volume server, still one master.
	two, grew, below := growShape(single, desiredShape(2))
	require.True(t, grew)
	require.False(t, below)
	require.Equal(t, 1, two.Masters)
	require.Equal(t, 2, two.VolumeServers)
	require.Equal(t, "001", two.Replication)

	// A third: the raft quorum.
	three, grew, _ := growShape(store.ObjectStore{Name: "s", Masters: 1, VolumeServers: 2, Replication: "001"}, desiredShape(3))
	require.True(t, grew)
	require.Equal(t, 3, three.Masters)
	require.Equal(t, 3, three.VolumeServers)

	// Losing a node never shrinks; it reads as below the shape.
	kept, grew, below := growShape(store.ObjectStore{Name: "s", Masters: 3, VolumeServers: 3, Replication: "001"}, desiredShape(2))
	require.False(t, grew)
	require.True(t, below)
	require.Equal(t, 3, kept.Masters)
	require.Equal(t, 3, kept.VolumeServers)
	require.Equal(t, "001", kept.Replication)

	// Mixed: a fleet that grew volume servers but not masters grows only
	// the dimension that is larger.
	mixed, grew, below := growShape(store.ObjectStore{Name: "s", Masters: 3, VolumeServers: 2, Replication: "001"}, desiredShape(4))
	require.True(t, grew)
	require.False(t, below)
	require.Equal(t, 3, mixed.Masters)
	require.Equal(t, 4, mixed.VolumeServers)
}
