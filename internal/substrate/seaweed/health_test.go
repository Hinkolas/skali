package seaweed

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestVolumeHealthCountsReplicas: against a recorded "001", a volume
// listed on two nodes is fine, one listed once is under replicated, and a
// volume still on "000" is both under replicated and unconfigured (the
// maintenance loop never gives it a copy until it is moved). Against a
// recorded "000" nothing is short and the "001" volumes are the ones off
// the recorded code. Shaped like the master's /vol/status on the pin (an
// empty placement object is "000", {"node":1} is "001").
func TestVolumeHealthCountsReplicas(t *testing.T) {
	t.Parallel()
	raw := `{"Volumes":{"DataCenters":{"dc":{"rack":{
		"node-a:8080":[
			{"Id":1,"Collection":"b-files","ReplicaPlacement":{"node":1}},
			{"Id":2,"Collection":"b-files","ReplicaPlacement":{"node":1}},
			{"Id":3,"Collection":"b-other","ReplicaPlacement":{}}],
		"node-b:8080":[
			{"Id":1,"Collection":"b-files","ReplicaPlacement":{"node":1}}]
	}}}}}`
	var vs volStatus
	require.NoError(t, json.Unmarshal([]byte(raw), &vs))
	replicated, err := parseReplication("001")
	require.NoError(t, err)
	require.Equal(t, VolumeHealth{Volumes: 3, UnderReplicated: 2, Unconfigured: 1, UnconfiguredIDs: []int64{3}},
		volumeHealth(vs, replicated))
	single, err := parseReplication("000")
	require.NoError(t, err)
	require.Equal(t, VolumeHealth{Volumes: 3, Unconfigured: 2, UnconfiguredIDs: []int64{1, 2}},
		volumeHealth(vs, single))
	require.Equal(t, 1, replicaPlacement{}.copies())
	require.Equal(t, 3, replicaPlacement{SameRackCount: 1, DiffDataCenterCount: 1}.copies())
}

// TestReplicationCodeRoundTrip: the three digits are data center, rack,
// node, and anything that is not three digits is refused.
func TestReplicationCodeRoundTrip(t *testing.T) {
	t.Parallel()
	placement, err := parseReplication("001")
	require.NoError(t, err)
	require.Equal(t, replicaPlacement{SameRackCount: 1}, placement)
	placement, err = parseReplication("100")
	require.NoError(t, err)
	require.Equal(t, replicaPlacement{DiffDataCenterCount: 1}, placement)
	for _, code := range []string{"000", "001", "010", "120"} {
		placement, err := parseReplication(code)
		require.NoError(t, err)
		require.Equal(t, code, placement.code())
	}
	for _, code := range []string{"", "01", "0001", "abc", "0a0"} {
		_, err := parseReplication(code)
		require.Error(t, err, code)
	}
}
