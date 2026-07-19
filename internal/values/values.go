// Package values implements the environment-values import model: dotenv
// parsing, validation against a compiled definition's value requirements, and
// plain/secret separation. Secrecy is declared by the manifest, never by the
// file. An empty value counts as unset, mirroring expression resolution, and
// defaults stay in the definition rather than being copied into the resolved
// set. The original file is an import format only; it is never persisted.
package values

import (
	"fmt"
	"maps"
	"path/filepath"
	"sort"
	"strings"

	"github.com/joho/godotenv"

	"github.com/Hinkolas/skali/internal/compiler"
)

// File is a parsed dotenv-style environment file.
type File struct {
	Path   string
	Values map[string]string
}

func ParseFile(path string) (*File, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve environment file path: %w", err)
	}
	parsed, err := godotenv.Read(absolute)
	if err != nil {
		return nil, fmt.Errorf("read environment file %s: %w", absolute, err)
	}
	return &File{Path: absolute, Values: parsed}, nil
}

func Parse(data []byte, path string) (*File, error) {
	parsed, err := godotenv.UnmarshalBytes(data)
	if err != nil {
		return nil, fmt.Errorf("parse environment file %s: %w", path, err)
	}
	return &File{Path: path, Values: parsed}, nil
}

// Resolved separates imported values by declared secrecy. Plain values become
// typed environment values; secret values enter the secret store and are
// represented elsewhere only by opaque references.
type Resolved struct {
	Plain  map[string]string
	Secret map[string]string
}

// Merged returns one map for local expression resolution and rendering.
// Remote deployments never use this form; revisions carry secret references.
func (r Resolved) Merged() map[string]string {
	merged := make(map[string]string, len(r.Plain)+len(r.Secret))
	maps.Copy(merged, r.Plain)
	maps.Copy(merged, r.Secret)
	return merged
}

type Options struct {
	// IgnoreUnknown drops keys the definition does not require instead of
	// rejecting the file. Ignored keys are never imported.
	IgnoreUnknown bool
}

// ValidationError reports every missing required and unknown key at once.
type ValidationError struct {
	Missing []string
	Unknown []string
}

func (e *ValidationError) Error() string {
	var problems []string
	if len(e.Missing) > 0 {
		problems = append(problems, "missing required project values: "+strings.Join(e.Missing, ", "))
	}
	if len(e.Unknown) > 0 {
		problems = append(problems, "unknown project values: "+strings.Join(e.Unknown, ", "))
	}
	return strings.Join(problems, "; ")
}

// Resolve validates a parsed file against the compiled definition's value
// requirements and separates the accepted values by secrecy.
func Resolve(requirements []compiler.VariableRequirement, file *File, options Options) (Resolved, error) {
	resolved := Resolved{Plain: map[string]string{}, Secret: map[string]string{}}
	failure := &ValidationError{}
	known := make(map[string]bool, len(requirements))
	for _, requirement := range requirements {
		known[requirement.Name] = true
		value := file.Values[requirement.Name]
		if value == "" {
			if requirement.Required {
				failure.Missing = append(failure.Missing, requirement.Name)
			}
			continue
		}
		if requirement.Secret {
			resolved.Secret[requirement.Name] = value
		} else {
			resolved.Plain[requirement.Name] = value
		}
	}
	if !options.IgnoreUnknown {
		for name := range file.Values {
			if !known[name] {
				failure.Unknown = append(failure.Unknown, name)
			}
		}
	}
	if len(failure.Missing) > 0 || len(failure.Unknown) > 0 {
		sort.Strings(failure.Missing)
		sort.Strings(failure.Unknown)
		return Resolved{}, failure
	}
	return resolved, nil
}
