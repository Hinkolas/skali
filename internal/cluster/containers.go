package cluster

import "errors"

// Container and image sentinels: what NodeHandle implementations wrap engine
// and RPC failures into, mapped to HTTP by the REST layer and to
// retry/backoff decisions by the reconciler.
var (
	ErrContainerNotFound = errors.New("cluster: container not found")
	ErrContainerConflict = errors.New("cluster: container name already in use")
	ErrInvalidSpec       = errors.New("cluster: invalid container spec")
	ErrImageNotFound     = errors.New("cluster: image not found")
	ErrImageInUse        = errors.New("cluster: image is in use")
	ErrInvalidRef        = errors.New("cluster: invalid image reference")
	ErrNodeUnreachable   = errors.New("cluster: node unreachable")
	ErrEngineUnavailable = errors.New("cluster: container engine unavailable on node")
)
