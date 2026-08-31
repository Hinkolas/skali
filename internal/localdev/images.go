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

// k3sBuiltinImages is the bundled-component list of the pinned k3s
// release (the k3s-images.txt asset of the K3sImage version): everything
// k3s deploys on its own at boot, from the pause sandbox to the Traefik
// edge. Nothing mirrors or caches these, so without them in the import
// batch a fresh cluster pulls all of them from docker.io (rate-limited,
// and the edge health gate waits on the Traefik pull). Bump in lockstep
// with K3sImage.
func k3sBuiltinImages() []string {
	return []string{
		"rancher/mirrored-pause:3.10.2",
		"rancher/mirrored-coredns-coredns:1.14.6",
		"rancher/klipper-helm:v0.13.3-build20260727",
		"rancher/mirrored-library-traefik:3.7.8",
		"rancher/klipper-lb:v0.4.17",
		"rancher/local-path-provisioner:v0.0.36",
		"rancher/mirrored-metrics-server:v0.9.0",
		"rancher/mirrored-library-busybox:1.37.0",
	}
}

// RequiredImages lists every public image the local platform needs inside
// the k3d node: the k3s built-in components, the CNPG operator, the
// bootstrap and shared-pool postgres images, the managed registry, and
// SeaweedFS. They are pre-pulled on the host and imported in one batch so
// a cold cluster never pulls from the internet mid-deploy; skalid and app
// images ride their own import and registry paths. The built-ins lead the
// list: k3s starts pulling them the moment it boots, so the earlier the
// import lands the more of that pull it saves (an image that arrives by
// either path makes the other a no-op).
func RequiredImages() []string {
	images := append(k3sBuiltinImages(),
		bundle.CNPGOperatorImage,
		bundle.BootstrapPostgresImage,
		bundle.RegistryImage,
		seaweed.Image,
	)
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
