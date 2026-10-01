// Package values implements the environment-values import model: dotenv
// parsing and the single contract check between a compiled definition's
// variable requirements and a provided value set. Every value is secret;
// there is no plain class. A present empty string is a real value, and
// defaults stay in the definition rather than being copied into the provided
// set. The original file is an import format only; it is never persisted.
package values

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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

// MissingError reports required values absent from a provided set.
type MissingError struct {
	Names []string
}

func (e *MissingError) Error() string {
	return "missing required project values: " + strings.Join(e.Names, ", ")
}

// Conform intersects a provided value set with the definition's variable
// requirements. kept is provided minus orphans; missing lists required names
// absent from provided; orphaned lists provided names no reference requires,
// sorted. Orphans are advisory, never errors: a stored value whose reference
// was removed must not block deployments.
func Conform[V any](requirements []compiler.VariableRequirement, provided map[string]V) (kept map[string]V, missing, orphaned []string) {
	known := make(map[string]bool, len(requirements))
	for _, requirement := range requirements {
		known[requirement.Name] = true
		if _, present := provided[requirement.Name]; !present && requirement.Required {
			missing = append(missing, requirement.Name)
		}
	}
	kept = make(map[string]V, len(provided))
	for name, value := range provided {
		if known[name] {
			kept[name] = value
		} else {
			orphaned = append(orphaned, name)
		}
	}
	sort.Strings(missing)
	sort.Strings(orphaned)
	return kept, missing, orphaned
}

// Fingerprint hashes a value set into a stable "sha256:<hex>" token: the
// sorted NAME=VALUE lines of the set, so key order, comments, and
// whitespace of the source file do not matter. Callers pass the conformed
// set (Conform's kept) so the fingerprint describes what a deployment would
// carry: a file change that only touches orphaned keys does not count, a
// manifest change that references a new key does. The token reveals no
// value; it lets a later run tell whether the same set would be deployed.
func Fingerprint(set map[string]string) string {
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	digest := sha256.New()
	for _, name := range names {
		digest.Write([]byte(name))
		digest.Write([]byte{'='})
		digest.Write([]byte(set[name]))
		digest.Write([]byte{'\n'})
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil))
}
