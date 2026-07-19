// Package revision assembles the immutable, environment-resolved revision a
// deployment targets: the canonical definition, non-secret values, opaque
// secret references, verified artifacts, and required target capabilities,
// sealed by a deterministic checksum. Building is pure: artifact resolution,
// clocks, and network access happen before Build, so the same inputs always
// produce the same checksum. Secret plaintext never enters a revision.
package revision

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"sort"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/layout"
	"github.com/Hinkolas/skali/internal/values"
)

const SchemaVersion = "1"

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
	Values          map[string]string          `json:"values,omitempty"`
	Secrets         map[string]SecretRef       `json:"secrets,omitempty"`
	Artifacts       map[string]Artifact        `json:"artifacts,omitempty"`
	Capabilities    []string                   `json:"capabilities"`
	Checksum        string                     `json:"checksum"`
}

// SecretRef represents a secret value without its plaintext. The name is the
// map key; the version identifies which stored secret generation the
// revision was resolved against.
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

type Input struct {
	Result      *compiler.Result
	Environment string
	Values      values.Resolved
	// SecretVersions selects the stored secret generation per name; a present
	// secret without an entry records version 1.
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

	if err := checkValues(definition, input.Values); err != nil {
		return nil, err
	}
	artifacts, err := checkArtifacts(definition, input.Artifacts)
	if err != nil {
		return nil, err
	}

	plain := make(map[string]string, len(input.Values.Plain))
	maps.Copy(plain, input.Values.Plain)
	secrets := make(map[string]SecretRef, len(input.Values.Secret))
	for name := range input.Values.Secret {
		version := input.SecretVersions[name]
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
		ValuesHash:      hashJSON(map[string]any{"values": plain, "secrets": secrets}),
		Values:          plain,
		Secrets:         secrets,
		Artifacts:       artifacts,
		Capabilities:    requiredCapabilities(definition),
	}
	revision.Checksum = hashJSON(revision)
	return revision, nil
}

// checkValues enforces the separation contract between the compiled
// requirements and the imported values: secrets only in the secret set, plain
// values only in the plain set, required values present, nothing unknown.
func checkValues(definition compiler.ProjectDefinition, resolved values.Resolved) error {
	known := make(map[string]bool, len(definition.RequiredVariables))
	for _, requirement := range definition.RequiredVariables {
		known[requirement.Name] = true
		_, plain := resolved.Plain[requirement.Name]
		_, secret := resolved.Secret[requirement.Name]
		if requirement.Secret {
			if plain {
				return fmt.Errorf("secret value %s must not appear in the plain value set", requirement.Name)
			}
			if !secret {
				return fmt.Errorf("missing secret value %s", requirement.Name)
			}
			continue
		}
		if secret {
			return fmt.Errorf("value %s is not declared secret but was imported as secret", requirement.Name)
		}
		if !plain && requirement.Required {
			return fmt.Errorf("missing required value %s", requirement.Name)
		}
	}
	for _, name := range sortedKeys(resolved.Plain) {
		if !known[name] {
			return fmt.Errorf("value %s is not required by the definition", name)
		}
	}
	for _, name := range sortedKeys(resolved.Secret) {
		if !known[name] {
			return fmt.Errorf("secret value %s is not required by the definition", name)
		}
	}
	return nil
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
	for _, key := range sortedKeys(provided) {
		if _, ok := definition.Applications[key]; !ok {
			return nil, fmt.Errorf("artifact %s does not match any application", key)
		}
	}
	return artifacts, nil
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
