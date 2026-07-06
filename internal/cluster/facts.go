package cluster

import (
	"runtime"

	"github.com/Hinkolas/skali/internal/clusterpb"
	"github.com/Hinkolas/skali/internal/hostinfo"
	"github.com/Hinkolas/skali/internal/version"
)

// heartbeatResponse assembles the static node facts plus the sampler's latest
// resource snapshot. Metrics stay nil until the sampler has two samples —
// rates need a delta — and the master leaves previous values in place.
func heartbeatResponse(nodeID string, sampler *hostinfo.Sampler) *clusterpb.HeartbeatResponse {
	resp := &clusterpb.HeartbeatResponse{
		NodeId:        nodeID,
		Arch:          runtime.GOARCH,
		Os:            runtime.GOOS,
		SkalidVersion: version.Version,
		CpuCount:      uint32(runtime.NumCPU()),
	}
	if snap, ok := sampler.Latest(); ok {
		resp.Metrics = metricsProto(snap)
	}
	return resp
}

func metricsProto(s hostinfo.Snapshot) *clusterpb.NodeMetrics {
	return &clusterpb.NodeMetrics{
		CpuPercent:           s.CPUPercent,
		MemoryUsedBytes:      s.MemUsed,
		MemoryTotalBytes:     s.MemTotal,
		DiskUsedBytes:        s.DiskUsed,
		DiskTotalBytes:       s.DiskTotal,
		NetRxBytesPerSec:     s.NetRxRate,
		NetTxBytesPerSec:     s.NetTxRate,
		DiskReadBytesPerSec:  s.DiskReadRate,
		DiskWriteBytesPerSec: s.DiskWriteRate,
		Load1:                s.Load1,
	}
}
