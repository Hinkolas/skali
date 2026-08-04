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
	"sort"
	"strings"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/layout"
	"github.com/Hinkolas/skali/internal/values"
)

const SchemaVersion = "2"

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
}

var environmentPattern = regexp.MustCompile("^[a-z][a-z0-9-]{0,62}$")

var digestPattern = regexp.MustCompile("^sha256:[0-9a-f]{64}$")

func Build(input Input) (*Revision, error) {
	if input.Result == nil {
		return nil, fmt.Errorf("revision requires a compiled definition")
	}
	if !environmentPattern.MatchString(input.Environment) {
		return nil, fmt.Errorf("invalid environment name %q", input.Environment)
	}
	definition := input.Result.Definition

	kept, err := checkValues(definition, input.SecretVersions)
	if err != nil {
		return nil, err
	}
	artifacts, err := checkArtifacts(definition, input.Artifacts)
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
// runtime requirements. Missing required values are the deployer's mistake
// and fail the build; orphaned values (stored but no longer referenced) are
// intersected away silently so a removed reference can never wedge an
// environment.
func checkValues(definition compiler.ProjectDefinition, provided map[string]int) (map[string]int, error) {
	kept, missing, _ := values.Conform(definition.RequiredVariables, provided)
	if len(missing) > 0 {
		return nil, valuesErrorf("missing required values: %s", strings.Join(missing, ", "))
	}
	return kept, nil
}

func checkArtifacts(definition compiler.ProjectDefinition, provided map[string]Artifact) (map[string]Artifact, error) {
	artifacts := make(map[string]Artifact, len(definition.Applications))
	for _, key := range sortedKeys(definition.Applications) {
		artifact, ok := provided[key]
		if !ok {
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
				// Expression-bearing image references are resolved during
				// artifact preparation, which records the resolved upstream.
				if !source.Image.IsLiteral() {
					return nil, fmt.Errorf("imported artifact for application %s must carry the resolved upstream reference", key)
				}
				artifact.Upstream = source.Image.Literal()
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
	for _, key := range sortedKeys(provided) {
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

func sortedKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
