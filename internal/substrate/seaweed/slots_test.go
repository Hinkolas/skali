package seaweed

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSlotReportReadsDirStatus: the directory topology's per-server Max and
// Volumes (shape measured on the pin) become per-server slots, sorted by
// server.
func TestSlotReportReadsDirStatus(t *testing.T) {
	t.Parallel()
	raw := `{"Topology":{"Max":30,"Free":20,"DataCenters":[{"Id":"DefaultDataCenter","Racks":[{"Id":"DefaultRack","DataNodes":[
		{"Url":"10.0.0.2:8080","PublicUrl":"10.0.0.2:8080","Volumes":4,"EcShards":0,"Max":10,"VolumeIds":" 1-4"},
		{"Url":"10.0.0.1:8080","PublicUrl":"10.0.0.1:8080","Volumes":6,"EcShards":0,"Max":20,"VolumeIds":" 1-6"}
	]}]}],"Layouts":[]},"Version":"30GB 4.48 530be3e37"}`
	var ds dirStatus
	require.NoError(t, json.Unmarshal([]byte(raw), &ds))
	report := slotReport(ds)
	require.Equal(t, []ServerSlots{
		{URL: "10.0.0.1:8080", Max: 20, Volumes: 6},
		{URL: "10.0.0.2:8080", Max: 10, Volumes: 4},
	}, report.Servers)
	require.Equal(t, 30, report.Total())
	require.Equal(t, 10, report.Used())
	require.Equal(t, 20, report.Free())
}

// TestVolumeHeadroomNeedsDistinctServers: every copy of a volume lands on
// a different server, so the headroom is bounded by the servers that
// still have room, not just the free total.
func TestVolumeHeadroomNeedsDistinctServers(t *testing.T) {
	t.Parallel()
	servers := func(free ...int) SlotReport {
		report := SlotReport{}
		for _, f := range free {
			report.Servers = append(report.Servers, ServerSlots{Max: f + 2, Volumes: 2})
		}
		return report
	}
	cases := []struct {
		name   string
		report SlotReport
		copies int
		want   int
	}{
		{"one copy takes every free slot", servers(5, 3), 1, 8},
		{"two copies pair the servers", servers(5, 5), 2, 5},
		{"the roomy server cannot pair with itself", servers(100, 3), 2, 3},
		{"three servers spread pairs", servers(4, 4, 4), 2, 6},
		{"a full server does not count", servers(10, 0), 2, 0},
		{"fewer servers than copies", servers(10), 2, 0},
		{"no servers", SlotReport{}, 1, 0},
		{"over-full servers have no room", SlotReport{Servers: []ServerSlots{{Max: 2, Volumes: 5}}}, 1, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, tc.report.VolumeHeadroom(tc.copies))
		})
	}
}

// TestBucketHeadroomFollowsReplicationAndGrowth: a new bucket grows
// VolumesPerBucket volumes, each with its replication's copies (the
// cluster from issue #289: six servers, no free slot left).
func TestBucketHeadroomFollowsReplicationAndGrowth(t *testing.T) {
	t.Parallel()
	report := SlotReport{Servers: []ServerSlots{{Max: 20, Volumes: 6}, {Max: 20, Volumes: 6}}}
	require.Equal(t, 14, report.BucketHeadroom("001", VolumesPerBucket(true)))
	require.Equal(t, 28, report.BucketHeadroom("000", VolumesPerBucket(true)))
	require.Equal(t, 4, report.BucketHeadroom("000", VolumesPerBucket(false)), "the dev all-in-one grows seven volumes a bucket")

	exhausted := SlotReport{}
	for range 6 {
		exhausted.Servers = append(exhausted.Servers, ServerSlots{Max: 4, Volumes: 4})
	}
	require.Zero(t, exhausted.BucketHeadroom("001", VolumesPerBucket(true)))
}

func TestVolumeSizeLimit(t *testing.T) {
	t.Parallel()
	require.Equal(t, DefaultVolumeSizeLimitMB, VolumeSizeLimitMB(nil))
	zero := int32(0)
	require.Equal(t, DefaultVolumeSizeLimitMB, VolumeSizeLimitMB(&zero))
	set := int32(1024)
	require.Equal(t, 1024, VolumeSizeLimitMB(&set))

	require.NoError(t, ValidateVolumeSizeLimitMB(DefaultVolumeSizeLimitMB))
	require.NoError(t, ValidateVolumeSizeLimitMB(MinVolumeSizeLimitMB))
	require.NoError(t, ValidateVolumeSizeLimitMB(MaxVolumeSizeLimitMB))
	require.Error(t, ValidateVolumeSizeLimitMB(MinVolumeSizeLimitMB-1))
	require.Error(t, ValidateVolumeSizeLimitMB(MaxVolumeSizeLimitMB+1))

	require.Equal(t, 2, ReplicationCopies("001"))
	require.Equal(t, 1, ReplicationCopies("000"))
	require.Equal(t, 1, ReplicationCopies("bogus"))
}
