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

// Import copies one upstream reference into the managed registry cache
// repository without a local daemon, preserving manifests and digests byte
// for byte (a docker pull/push round trip would re-serialize and change
// them). Multi-platform indexes are copied whole so a digest-pinned pull
// works from any node architecture. Both sides authenticate through the
// default docker keychain, so one docker login covers a token-gated
// managed registry and private upstreams alike; hosts without a stored
// login stay anonymous (the local registry).
func Import(ctx context.Context, upstream, targetRef string, insecureTarget bool, sink ProgressSink) (ImportResult, error) {
	srcRef, err := name.ParseReference(upstream)
	if err != nil {
		return ImportResult{}, fmt.Errorf("build: parse upstream %s: %w", upstream, err)
	}
	var dstOpts []name.Option
	if insecureTarget {
		dstOpts = append(dstOpts, name.Insecure)
	}
	dstRef, err := name.ParseReference(targetRef, dstOpts...)
	if err != nil {
		return ImportResult{}, fmt.Errorf("build: parse target %s: %w", targetRef, err)
	}
	remoteOpts := []remote.Option{
		remote.WithContext(ctx),
		remote.WithAuthFromKeychain(authn.DefaultKeychain),
	}

	sink.Line("info", "resolving "+srcRef.String())
	descriptor, err := remote.Get(srcRef, remoteOpts...)
	if err != nil {
		return ImportResult{}, fmt.Errorf("build: resolve %s: %w", upstream, err)
	}
	sink.Line("info", "importing "+descriptor.Digest.String())
	if descriptor.MediaType.IsIndex() {
		index, err := descriptor.ImageIndex()
		if err != nil {
			return ImportResult{}, fmt.Errorf("build: read index %s: %w", upstream, err)
		}
		if err := remote.WriteIndex(dstRef, index, remoteOpts...); err != nil {
			return ImportResult{}, fmt.Errorf("build: push index to %s: %w", targetRef, err)
		}
	} else {
		image, err := descriptor.Image()
		if err != nil {
			return ImportResult{}, fmt.Errorf("build: read image %s: %w", upstream, err)
		}
		if err := remote.Write(dstRef, image, remoteOpts...); err != nil {
			return ImportResult{}, fmt.Errorf("build: push image to %s: %w", targetRef, err)
		}
	}
	return ImportResult{Digest: descriptor.Digest.String()}, nil
}
