package build

import (
	"context"
	"fmt"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// ImportResult reports one completed import.
type ImportResult struct {
	// Digest is the manifest or index digest, identical upstream and in
	// the managed registry: imports preserve manifest bytes exactly.
	Digest string
}

// ImportOptions configures the managed-registry side of an import.
type ImportOptions struct {
	// InsecureTarget permits plain HTTP toward the managed registry (the
	// loopback-only local registry).
	InsecureTarget bool
	// TargetAuth authenticates the push toward the managed registry (the
	// remote session credential); nil falls back to the default docker
	// keychain, which is anonymous without a stored login.
	TargetAuth authn.Authenticator
}

// Import copies one upstream reference into the managed registry cache
// repository without a local daemon, preserving manifests and digests byte
// for byte (a docker pull/push round trip would re-serialize and change
// them). Multi-platform indexes are copied whole so a digest-pinned pull
// works from any node architecture. The upstream pull uses the default
// docker keychain, so private upstreams work with an ordinary docker login
// for that upstream.
func Import(ctx context.Context, upstream, targetRef string, opts ImportOptions, sink ProgressSink) (ImportResult, error) {
	srcRef, err := name.ParseReference(upstream)
	if err != nil {
		return ImportResult{}, fmt.Errorf("build: parse upstream %s: %w", upstream, err)
	}
	var dstNameOpts []name.Option
	if opts.InsecureTarget {
		dstNameOpts = append(dstNameOpts, name.Insecure)
	}
	dstRef, err := name.ParseReference(targetRef, dstNameOpts...)
	if err != nil {
		return ImportResult{}, fmt.Errorf("build: parse target %s: %w", targetRef, err)
	}

	srcOpts := []remote.Option{
		remote.WithContext(ctx),
		remote.WithAuthFromKeychain(authn.DefaultKeychain),
	}
	dstOpts := []remote.Option{remote.WithContext(ctx)}
	if opts.TargetAuth != nil {
		dstOpts = append(dstOpts, remote.WithAuth(opts.TargetAuth))
	} else {
		dstOpts = append(dstOpts, remote.WithAuthFromKeychain(authn.DefaultKeychain))
	}

	sink.Line("info", "resolving "+srcRef.String())
	descriptor, err := remote.Get(srcRef, srcOpts...)
	if err != nil {
		return ImportResult{}, fmt.Errorf("build: resolve %s: %w", upstream, err)
	}
	sink.Line("info", "importing "+descriptor.Digest.String())
	if descriptor.MediaType.IsIndex() {
		index, err := descriptor.ImageIndex()
		if err != nil {
			return ImportResult{}, fmt.Errorf("build: read index %s: %w", upstream, err)
		}
		if err := remote.WriteIndex(dstRef, index, dstOpts...); err != nil {
			return ImportResult{}, fmt.Errorf("build: push index to %s: %w", targetRef, err)
		}
	} else {
		image, err := descriptor.Image()
		if err != nil {
			return ImportResult{}, fmt.Errorf("build: read image %s: %w", upstream, err)
		}
		if err := remote.Write(dstRef, image, dstOpts...); err != nil {
			return ImportResult{}, fmt.Errorf("build: push image to %s: %w", targetRef, err)
		}
	}
	return ImportResult{Digest: descriptor.Digest.String()}, nil
}
