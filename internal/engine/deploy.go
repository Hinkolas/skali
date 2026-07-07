package engine

import (
	"context"
	"fmt"
)

// Deploy is the one create path: resolve the image per the pull policy,
// create, optionally start, and return the resulting container. The node's
// gRPC server and the master's local handle both go through it so create
// semantics can't drift between the two.
//
// A container that was created but failed to start is returned alongside the
// error — it exists on the node and the caller decides its fate.
func Deploy(ctx context.Context, eng Engine, spec ContainerSpec, pull PullPolicy, start bool) (Container, error) {
	switch pull {
	case PullAlways:
		if err := eng.Pull(ctx, spec.Image); err != nil {
			return Container{}, err
		}
	case PullIfMissing, "":
		exists, err := eng.ImageExists(ctx, spec.Image)
		if err != nil {
			return Container{}, err
		}
		if !exists {
			if err := eng.Pull(ctx, spec.Image); err != nil {
				return Container{}, err
			}
		}
	case PullNever:
		// Create fails with ErrImageMissing when the image is absent.
	default:
		return Container{}, fmt.Errorf("engine: invalid pull policy %q", pull)
	}

	id, err := eng.Create(ctx, spec)
	if err != nil {
		return Container{}, err
	}
	if start {
		if err := eng.Start(ctx, id); err != nil {
			c, _ := eng.Inspect(ctx, id)
			return c, fmt.Errorf("engine: container created but failed to start: %w", err)
		}
	}
	return eng.Inspect(ctx, id)
}
