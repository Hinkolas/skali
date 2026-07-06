//go:build !linux

package cluster

// Production nodes are linux; elsewhere (dev machines) memory just reads 0.
func totalMemoryBytes() uint64 { return 0 }
