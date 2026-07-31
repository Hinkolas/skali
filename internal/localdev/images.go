package localdev

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"

	"github.com/Hinkolas/skali/internal/bundle"
	"github.com/Hinkolas/skali/internal/substrate/cnpg"
	"github.com/Hinkolas/skali/internal/substrate/seaweed"
)

// RequiredImages lists every public image the local platform needs inside
// the k3d node: the CNPG operator, the bootstrap and shared-pool postgres
// images, the managed registry, and SeaweedFS. They are pre-pulled on the
// host and imported in one batch so a cold cluster never pulls from the
// internet mid-deploy; skalid and app images ride their own import and
// registry paths.
func RequiredImages() []string {
	images := []string{
		bundle.CNPGOperatorImage,
		bundle.BootstrapPostgresImage,
		bundle.RegistryImage,
		seaweed.Image,
	}
	// The default shared-pool image; substrate.DefaultEngine/DefaultMajor
	// stay unimported here (a drift test in internal/substrate pins the
	// agreement).
	if image, ok := cnpg.Lookup("postgres", 17); ok && image.Ref != bundle.BootstrapPostgresImage {
		images = append(images, image.Ref)
	}
	return images
}

// EnsureHostImage makes one public image present in the local docker
// daemon: a cache hit is free and keeps offline hosts working, only a miss
// pulls. The host daemon is the durable cache; it survives even a
// `skali dev reset`.
func EnsureHostImage(ctx context.Context, image string) error {
	if err := exec.CommandContext(ctx, "docker", "image", "inspect", "--format", "{{.Id}}", image).Run(); err == nil {
		return nil
	}
	if out, err := exec.CommandContext(ctx, "docker", "pull", image).CombinedOutput(); err != nil {
		return fmt.Errorf("localdev: docker pull %s: %w\n%s", image, err, out)
	}
	return nil
}

// ensureHostImages pulls the missing images concurrently; every error names
// its image.
func ensureHostImages(ctx context.Context, images []string) error {
	var wg sync.WaitGroup
	errs := make([]error, len(images))
	for i, image := range images {
		wg.Go(func() {
			errs[i] = EnsureHostImage(ctx, image)
		})
	}
	wg.Wait()
	return errors.Join(errs...)
}
