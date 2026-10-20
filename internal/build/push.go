package build

import (
	"context"
	"fmt"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
)

// pushRemoteOptions is the shared destination policy of every managed-registry
// upload (builds here, imports in import.go): the caller's authenticator when
// present, else the default docker keychain, which is anonymous without a
// stored login and satisfies the loopback dev registry. The upload's
// transport lets it follow the push.
func pushRemoteOptions(ctx context.Context, auth authn.Authenticator, up *upload) []remote.Option {
	opts := []remote.Option{remote.WithContext(ctx), remote.WithTransport(up.transport())}
	if auth != nil {
		return append(opts, remote.WithAuth(auth))
	}
	return append(opts, remote.WithAuthFromKeychain(authn.DefaultKeychain))
}

// pushLayout uploads the artifact of an exported OCI layout directory to
// targetRef and returns its digest. The layout carries BuildKit's compressed
// blobs with precomputed digests, so blobs the registry already has are
// skipped without recompression. The exported index must carry exactly one
// artifact (provenance and SBOM manifests are disabled at build time).
func pushLayout(ctx context.Context, dir, targetRef string, auth authn.Authenticator, sink ProgressSink) (string, error) {
	up := beginUpload(sink, targetRef)
	digest, err := func() (string, error) {
		index, err := layout.ImageIndexFromPath(dir)
		if err != nil {
			return "", fmt.Errorf("build: read exported layout: %w", err)
		}
		manifest, err := index.IndexManifest()
		if err != nil {
			return "", fmt.Errorf("build: read exported layout index: %w", err)
		}
		if len(manifest.Manifests) != 1 {
			return "", fmt.Errorf("build: exported layout carries %d manifests, want exactly 1", len(manifest.Manifests))
		}
		descriptor := manifest.Manifests[0]
		ref, err := name.ParseReference(targetRef)
		if err != nil {
			return "", fmt.Errorf("build: parse push reference %s: %w", targetRef, err)
		}
		opts := pushRemoteOptions(ctx, auth, up)
		if descriptor.MediaType.IsIndex() {
			nested, err := index.ImageIndex(descriptor.Digest)
			if err != nil {
				return "", fmt.Errorf("build: read exported index %s: %w", descriptor.Digest, err)
			}
			inventory, err := inventoryIndex(nested)
			up.plan(descriptor.Digest.String(), inventory, err)
			if err := remote.WriteIndex(ref, nested, opts...); err != nil {
				return "", fmt.Errorf("build: push index to %s: %w", targetRef, err)
			}
		} else {
			image, err := index.Image(descriptor.Digest)
			if err != nil {
				return "", fmt.Errorf("build: read exported image %s: %w", descriptor.Digest, err)
			}
			inventory, err := inventoryImage(image)
			up.plan(descriptor.Digest.String(), inventory, err)
			if err := remote.Write(ref, image, opts...); err != nil {
				return "", fmt.Errorf("build: push image to %s: %w", targetRef, err)
			}
		}
		return descriptor.Digest.String(), nil
	}()
	return digest, up.end(ctx, err)
}

// pushDockerSave uploads a docker-save tarball to targetRef and returns the
// pushed manifest digest. Saved layers are uncompressed, so they are gzipped
// here; the compression is deterministic, so repeated pushes of the same
// image yield the same digest.
func pushDockerSave(ctx context.Context, tarPath, targetRef string, auth authn.Authenticator, sink ProgressSink) (string, error) {
	up := beginUpload(sink, targetRef)
	digest, err := func() (string, error) {
		image, err := tarball.ImageFromPath(tarPath, nil)
		if err != nil {
			return "", fmt.Errorf("build: read saved image: %w", err)
		}
		// The digest covers the compressed layers, so computing it
		// compresses every layer once: the long part of preparing.
		sink.Line("info", "compressing the saved image layers")
		digest, err := image.Digest()
		if err != nil {
			return "", fmt.Errorf("build: digest saved image: %w", err)
		}
		ref, err := name.ParseReference(targetRef)
		if err != nil {
			return "", fmt.Errorf("build: parse push reference %s: %w", targetRef, err)
		}
		inventory, err := inventoryImage(image)
		up.plan(digest.String(), inventory, err)
		if err := remote.Write(ref, image, pushRemoteOptions(ctx, auth, up)...); err != nil {
			return "", fmt.Errorf("build: push image to %s: %w", targetRef, err)
		}
		return digest.String(), nil
	}()
	return digest, up.end(ctx, err)
}
