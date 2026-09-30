package seaweed

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCollectionStatsChargesLiveBytes: replicas are deduped by volume id,
// deleted bytes and entries the volume servers already account for stop
// counting, and the disk footprint stays visible separately.
func TestCollectionStatsChargesLiveBytes(t *testing.T) {
	t.Parallel()
	raw := `{"Volumes":{"DataCenters":{"dc":{"rack":{
		"node-a:8080":[
			{"Id":1,"Size":1000,"Collection":"b-files","FileCount":4,"DeleteCount":1,"DeletedByteCount":300},
			{"Id":2,"Size":500,"Collection":"b-files","FileCount":2,"DeleteCount":0,"DeletedByteCount":0},
			{"Id":3,"Size":50,"Collection":"","FileCount":1}],
		"node-b:8080":[
			{"Id":1,"Size":1000,"Collection":"b-files","FileCount":4,"DeleteCount":1,"DeletedByteCount":300}]
	}}}}}`
	var vs volStatus
	require.NoError(t, json.Unmarshal([]byte(raw), &vs))
	stats := collectionStats(vs)
	require.Len(t, stats, 1, "the collection-less system volume is not a bucket")
	require.Equal(t, CollectionStat{SizeBytes: 1500, LiveBytes: 1200, DeletedBytes: 300, EntryCount: 5}, stats["b-files"])
}
