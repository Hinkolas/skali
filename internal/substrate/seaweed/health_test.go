package seaweed

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestVolumeHealthCountsReplicas: a volume listed on two nodes with a
// one-copy placement is fine, one listed once with "001" is under
// replicated, and an unreplicated volume never is. Shaped like the
// master's /vol/status on the pin (an empty placement object is "000",
// {"node":1} is "001").
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
	health := volumeHealth(vs)
	require.Equal(t, VolumeHealth{Volumes: 3, UnderReplicated: 1}, health)
	require.Equal(t, 1, replicaPlacement{}.copies())
	require.Equal(t, 3, replicaPlacement{SameRackCount: 1, DiffDataCenterCount: 1}.copies())
}
