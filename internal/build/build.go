// Package build is the pure build engine layer: context collection with
// deterministic input hashing, an executor-neutral Engine interface, the
// docker (BuildKit through buildx) implementation, and registry-to-registry
// image imports. It knows nothing about jobs, queues, Postgres, or the
// public API; the CLI drives it directly in R3 and the build worker drives
// the same code in R4. Secret build inputs pass through BuildKit secret
// mounts and process environments only; they never appear in command lines,
// image history, or hashes.
package build

import (
	"context"
	"encoding/json"
)

// BuildRequest is one fully resolved build: every path is absolute, every
// argument value is a resolved plain string.
type BuildRequest struct {
	// ContextDir is the collected build context root.
	ContextDir string
	// Dockerfile is the absolute path of the Dockerfile; it may live
	// outside the context directory (both are project-root-relative in the
	// manifest).
	Dockerfile string
	// Target selects a multi-stage build target; empty builds the final
	// stage.
	Target string
	// Arguments are plain build arguments passed as --build-arg.
	Arguments map[string]string
	// SecretEnv maps BuildKit secret ids to their values. Values reach the
	// builder through the child process environment and secret mounts,
	// never through arguments or Dockerfile ARG.
	SecretEnv map[string]string
	// Platform is the target platform (for example linux/arm64); empty
	// uses the builder default.
	Platform string
	// PushRef is the full managed-registry reference the result is pushed
	// to.
	PushRef string
}

// Result reports one verified-pushable build output.
type Result struct {
	// Digest is the pushed manifest digest (sha256:...). The server side
	// verifies it against the registry before trusting it.
	Digest string
	// Provenance is engine-specific detail recorded on the artifact.
	Provenance json.RawMessage
}

// ProgressSink receives the engine's line-wise build progress; callers
// forward it into the journal.
type ProgressSink interface {
	Line(level, message string)
}

// Engine executes one build and pushes the result.
type Engine interface {
	Build(ctx context.Context, req BuildRequest, sink ProgressSink) (Result, error)
}

// DiscardSink drops progress; useful for probes and tests.
type DiscardSink struct{}

func (DiscardSink) Line(string, string) {}
