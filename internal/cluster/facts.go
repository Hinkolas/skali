package cluster

import (
	"runtime"

	"github.com/Hinkolas/skali/internal/clusterpb"
	"github.com/Hinkolas/skali/internal/version"
)

// nodeFacts are the static machine facts reported in heartbeats.
func nodeFacts(nodeID string) *clusterpb.HeartbeatResponse {
	return &clusterpb.HeartbeatResponse{
		NodeId:        nodeID,
		Arch:          runtime.GOARCH,
		Os:            runtime.GOOS,
		SkalidVersion: version.Version,
		CpuCount:      uint32(runtime.NumCPU()),
		MemoryBytes:   totalMemoryBytes(),
	}
}
