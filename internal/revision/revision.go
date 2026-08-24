// Package revision assembles the immutable, environment-resolved revision a
// deployment targets: the canonical definition, opaque value references,
// verified artifacts, and required target capabilities, sealed by a
// deterministic checksum. Building is pure: artifact resolution, clocks, and
// network access happen before Build, so the same inputs always produce the
// same checksum. Value plaintext never enters a revision; every value is
// pinned as a (name, version) reference into the encrypted store.
package revision

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/layout"
	"github.com/Hinkolas/skali/internal/manifest"
	"github.com/Hinkolas/skali/internal/naming"
	"github.com/Hinkolas/skali/internal/utils"
	"github.com/Hinkolas/skali/internal/values"
)

// SchemaVersion follows the single version knob in manifest.CurrentVersion:
// stored revision documents carry it, and Decode accepts exactly that value.
const SchemaVersion = manifest.CurrentVersion

// Artifact kinds. Imported upstream content is a reconstructable cache;
// locally or cloud-built artifacts may be the only deployable copy.
const (
	KindImport     = "import"
	KindBuildLocal = "build-local"
	KindBuildCloud = "build-cloud"
)

type Revision struct {
	SchemaVersion   string                     `json:"schemaVersion"`
	Project         string                     `json:"project"`
	Environment     string                     `json:"environment"`
	CompilerVersion string                     `json:"compilerVersion"`
	DefinitionHash  string                     `json:"definitionHash"`
	Definition      compiler.ProjectDefinition `json:"definition"`
	ValuesHash      string                     `json:"valuesHash"`
	Secrets         map[string]SecretRef       `json:"secrets,omitempty"`
	Artifacts       map[string]Artifact        `json:"artifacts,omitempty"`
	Capabilities    []string                   `json:"capabilities"`
	Checksum        string                     `json:"checksum"`
}

// SecretRef represents a value without its plaintext. The name is the map
// key; the version identifies which stored generation the revision was
// resolved against.
type SecretRef struct {
	Version int `json:"version"`
}

// Artifact is the verified, content-addressed result of preparing one
// application's image: an import from an upstream reference or a local or
// cloud build. Cluster workloads pull the managed reference by digest.
type Artifact struct {
	Reference   string `json:"reference"`
	Digest      string `json:"digest"`
	Kind        string `json:"kind"`
	Upstream    string `json:"upstream,omitempty"`
	ContextHash string `json:"contextHash,omitempty"`
	// Platforms lists the platforms the image runs on: a build's target
	// platforms, or an image source's declared platforms. The renderer
	// derives node arch affinity from it; empty means unknown and renders
	// no constraint.
	Platforms []string `json:"platforms,omitempty"`
}

// Equal reports whether two artifacts are identical, including their
// platform sets. Artifact stopped being comparable with == when Platforms
// was added.
func (a Artifact) Equal(b Artifact) bool {
	return a.Reference == b.Reference && a.Digest == b.Digest && a.Kind == b.Kind &&
		a.Upstream == b.Upstream && a.ContextHash == b.ContextHash &&
		slices.Equal(a.Platforms, b.Platforms)
}

// PendingDigest stands in for an artifact whose build or import has not run
// yet, so a candidate revision can be constructed to compute a plan. It never
// enters a stored revision, and plans must describe it as pending work rather
// than print it as a hash.
const PendingDigest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"

type Input struct {
	Result      *compiler.Result
	Environment string
	// SecretVersions is the provided value set: the stored generation per
	// name. The caller resolves it against the store; Build intersects it
	// with the definition's runtime requirements. A name with version 0
	// records version 1.
	SecretVersions  map[string]int
	Artifacts       map[string]Artifact
	CompilerVersion string
	// LocalApplications marks applications deployed as host-run intercepts
	// (skali dev): they carry no artifact and checkArtifacts tolerates the
	// absence. The zero value keeps every other caller strict; only the
	// local-platform deploy path ever sets it.
	LocalApplications map[string]bool
}

var digestPattern = regexp.MustCompile("^sha256:[0-9a-f]{64}$")

func Build(input Input) (*Revision, error) {
	if input.Result == nil {
		return nil, fmt.Errorf("revision requires a compiled definition")
	}
	if err := naming.CheckKey(input.Environment); err != nil {
		return nil, fmt.Errorf("invalid environment name %q: %v", input.Environment, err)
	}
	definition := input.Result.Definition

	kept, err := checkValues(definition, input.SecretVersions)
	if err != nil {
		return nil, err
	}
	artifacts, err := checkArtifacts(definition, input.Artifacts, input.LocalApplications)
	if err != nil {
		return nil, err
	}

	secrets := make(map[string]SecretRef, len(kept))
	for name, version := range kept {
		if version == 0 {
			version = 1
		}
		secrets[name] = SecretRef{Version: version}
	}

	revision := &Revision{
		SchemaVersion:   SchemaVersion,
		Project:         definition.Name,
		Environment:     input.Environment,
		CompilerVersion: input.CompilerVersion,
		DefinitionHash:  input.Result.Hash,
		Definition:      definition,
		ValuesHash:      hashJSON(secrets),
		Secrets:         secrets,
		Artifacts:       artifacts,
		Capabilities:    requiredCapabilities(definition),
	}
	revision.Checksum = hashJSON(revision)
	return revision, nil
}

// SchemaError reports a stored revision document written under a different
// schema version than this build supports. Callers may surface the message
// verbatim.
type SchemaError struct {
	Got  string
	Want string
}

func (e *SchemaError) Error() string {
	return fmt.Sprintf("revision schema %q is not supported by this build (expected %q); redeploy the environment", e.Got, e.Want)
}

// Decode unmarshals a stored revision document, accepting exactly the
// current SchemaVersion. The version is peeked before the full unmarshal
// because a document from another schema generation may not even fit the
// current struct shapes; the version mismatch is the error worth reporting.
func Decode(document []byte) (*Revision, error) {
	var peek struct {
		SchemaVersion string `json:"schemaVersion"`
	}
	if err := json.Unmarshal(document, &peek); err != nil {
		return nil, fmt.Errorf("decode revision document: %w", err)
	}
	if peek.SchemaVersion != SchemaVersion {
		return nil, &SchemaError{Got: peek.SchemaVersion, Want: SchemaVersion}
	}
	var decoded Revision
	if err := json.Unmarshal(document, &decoded); err != nil {
		return nil, fmt.Errorf("decode revision document: %w", err)
	}
	return &decoded, nil
}

// ValuesError reports that the environment's values do not satisfy the
// definition's requirements. It is the deployer's mistake, not an internal
// failure, so callers may surface the message verbatim.
type ValuesError struct {
	Message string
}

func (e *ValuesError) Error() string { return e.Message }

func valuesErrorf(format string, args ...any) error {
	return &ValuesError{Message: fmt.Sprintf(format, args...)}
}

// checkValues intersects the provided value set with the definition's
// requirements. Missing required values are the deployer's mistake and fail
// the build; orphaned values (stored but no longer referenced) are
// intersected away silently so a removed reference can never wedge an
// environment.
func checkValues(definition compiler.ProjectDefinition, provided map[string]int) (map[string]int, error) {
	kept, missing, _ := values.Conform(definition.RequiredVariables, provided)
	if len(missing) > 0 {
		return nil, valuesErrorf("missing required values: %s", strings.Join(missing, ", "))
	}
	return kept, nil
}

func checkArtifacts(definition compiler.ProjectDefinition, provided map[string]Artifact, local map[string]bool) (map[string]Artifact, error) {
	artifacts := make(map[string]Artifact, len(definition.Applications))
	for _, key := range utils.SortedKeys(definition.Applications) {
		artifact, ok := provided[key]
		if !ok {
			if local[key] {
				// Host-run intercepts ship no artifact; the revision
				// records the absence and its checksum changes with it.
				continue
			}
			return nil, fmt.Errorf("application %s has no prepared artifact", key)
		}
		if artifact.Reference == "" {
			return nil, fmt.Errorf("artifact for application %s has no managed-registry reference", key)
		}
		if !digestPattern.MatchString(artifact.Digest) {
			return nil, fmt.Errorf("artifact for application %s has an invalid digest %q", key, artifact.Digest)
		}
		source := definition.Applications[key].Source
		switch source.Kind {
		case "image":
			if artifact.Kind != KindImport {
				return nil, fmt.Errorf("application %s uses an image source; its artifact kind must be %s", key, KindImport)
			}
			if artifact.Upstream == "" {
				artifact.Upstream = source.Image
			}
			if artifact.ContextHash != "" {
				return nil, fmt.Errorf("imported artifact for application %s must not carry a build-context hash", key)
			}
		default:
			if artifact.Kind != KindBuildLocal && artifact.Kind != KindBuildCloud {
				return nil, fmt.Errorf("application %s uses a build source; its artifact kind must be %s or %s", key, KindBuildLocal, KindBuildCloud)
			}
			if artifact.Upstream != "" {
				return nil, fmt.Errorf("built artifact for application %s must not carry an upstream reference", key)
			}
		}
		artifacts[key] = artifact
	}
	for _, key := range utils.SortedKeys(provided) {
		if _, ok := definition.Applications[key]; !ok {
			return nil, fmt.Errorf("artifact %s does not match any application", key)
		}
	}
	return artifacts, nil
}

// RequiredCapabilities exposes the capability derivation for pre-revision
// gating: deployment open checks the installation's declared capabilities
// before any artifact work starts.
func RequiredCapabilities(definition compiler.ProjectDefinition) []string {
	return requiredCapabilities(definition)
}

// requiredCapabilities derives the node capabilities a revision needs from
// the definition, in the layout package's display order.
func requiredCapabilities(definition compiler.ProjectDefinition) []string {
	needed := make(map[string]bool, len(layout.Capabilities))
	needed[layout.CapabilityApplication] = len(definition.Applications) > 0
	needed[layout.CapabilityDatabase] = len(definition.Databases) > 0
	needed[layout.CapabilityObjectStorage] = len(definition.Buckets) > 0
	for _, application := range definition.Applications {
		if len(application.Routes) > 0 {
			needed[layout.CapabilityEdge] = true
		}
	}
	capabilities := make([]string, 0, len(layout.Capabilities))
	for _, capability := range layout.Capabilities {
		if needed[capability] {
			capabilities = append(capabilities, capability)
		}
	}
	return capabilities
}

// hashJSON hashes the canonical JSON encoding; encoding/json sorts map keys,
// so equal content always hashes equally. The revision checksum is computed
// with the checksum field still empty.
func hashJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("encode for hashing: %v", err))
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
