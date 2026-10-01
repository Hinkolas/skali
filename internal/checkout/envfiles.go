package checkout

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/Hinkolas/skali/internal/filelock"
)

// envFilesFilename holds the per-environment env file memory under .skali/:
// which local dotenv file (or the stored values) each environment deploys
// with, and a fingerprint of the values the last deployment from this
// checkout carried. Like the binding it is disposable local tool state and
// stores no secrets: paths and a hash, never a value.
const envFilesFilename = "env-files.yaml"

// envFilesHeader explains the file to whoever opens it.
const envFilesHeader = "# Skali env file memory: which local env file each environment deploys with.\n" +
	"# Local tool state, safe to delete; the next deploy or dev asks again.\n"

// EnvFileChoice is one environment's remembered value source. Exactly one
// of File and Stored is set: File names the dotenv file relative to the
// project root, Stored means the environment's stored values. Fingerprint
// hashes the conformed value set the last deployment from this checkout
// carried, so a later run can say whether a file changed since; it is
// empty until a deployment has been opened.
type EnvFileChoice struct {
	File        string `yaml:"file,omitempty"`
	Stored      bool   `yaml:"stored,omitempty"`
	Fingerprint string `yaml:"fingerprint,omitempty"`
}

type envFilesState struct {
	SchemaVersion int                      `yaml:"schemaVersion"`
	Environments  map[string]EnvFileChoice `yaml:"environments"`
}

// EnvFilesPath returns the memory file location under the project root.
func EnvFilesPath(root string) string {
	return filepath.Join(Dir(root), envFilesFilename)
}

// LoadEnvFiles reads the env file memory; an empty map when none exists.
// A corrupt file, or one naming a path outside the project root, is an
// error naming the fix.
func LoadEnvFiles(root string) (map[string]EnvFileChoice, error) {
	path := EnvFilesPath(root)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]EnvFileChoice{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("checkout: read %s: %w", path, err)
	}
	state, err := parseEnvFiles(data)
	if err != nil {
		return nil, fmt.Errorf("checkout: invalid env file memory %s: %v; repair it or delete it to pick again", path, err)
	}
	return state.Environments, nil
}

func parseEnvFiles(data []byte) (*envFilesState, error) {
	var state envFilesState
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&state); err != nil {
		return nil, err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, errors.New("expected one YAML document")
	}
	if state.SchemaVersion != 1 {
		return nil, errors.New("expected schemaVersion: 1")
	}
	if state.Environments == nil {
		state.Environments = map[string]EnvFileChoice{}
	}
	for environment, choice := range state.Environments {
		if environment == "" {
			return nil, errors.New("environment names must not be empty")
		}
		if err := choice.validate(); err != nil {
			return nil, fmt.Errorf("environment %s: %w", environment, err)
		}
	}
	return &state, nil
}

// validate checks a choice's shape: either a file inside the project root
// or the stored values, never both and never neither.
func (c EnvFileChoice) validate() error {
	switch {
	case c.File == "" && !c.Stored:
		return errors.New("expected file or stored: true")
	case c.File != "" && c.Stored:
		return errors.New("file and stored: true are mutually exclusive")
	case c.File == "":
		return nil
	}
	if filepath.IsAbs(c.File) || strings.HasPrefix(filepath.ToSlash(filepath.Clean(c.File)), "../") ||
		filepath.Clean(c.File) == ".." || filepath.Clean(c.File) == "." {
		return fmt.Errorf("file %q must be a path inside the project root", c.File)
	}
	return nil
}

// Resolve returns the choice's file as an absolute path under root; empty
// for the stored values.
func (c EnvFileChoice) Resolve(root string) string {
	if c.File == "" {
		return ""
	}
	return filepath.Join(root, filepath.FromSlash(c.File))
}

// SaveEnvFileChoice records an environment's choice, merging it into the
// other environments' entries under a lock. An absolute File is stored
// relative to root; an empty Fingerprint keeps the existing entry's, so
// re-picking the same file does not forget what was last deployed.
func SaveEnvFileChoice(root, environment string, choice EnvFileChoice) error {
	if environment == "" {
		return errors.New("checkout: env file memory needs an environment name")
	}
	if filepath.IsAbs(choice.File) {
		relative, err := filepath.Rel(root, choice.File)
		if err != nil {
			return fmt.Errorf("checkout: env file memory: %w", err)
		}
		choice.File = filepath.ToSlash(relative)
	}
	if err := choice.validate(); err != nil {
		return fmt.Errorf("checkout: env file memory: %w", err)
	}
	if err := EnsureDir(root); err != nil {
		return err
	}
	path := EnvFilesPath(root)
	unlock, err := filelock.Acquire(context.Background(), path+".lock")
	if err != nil {
		return err
	}
	defer unlock()
	environments, err := LoadEnvFiles(root)
	if err != nil {
		return err
	}
	if choice.Fingerprint == "" {
		if previous, ok := environments[environment]; ok && previous.Fingerprint != "" {
			choice.Fingerprint = previous.Fingerprint
		}
	}
	environments[environment] = choice
	return writeEnvFiles(root, path, &envFilesState{SchemaVersion: 1, Environments: environments})
}

// SaveEnvFileFingerprint records the fingerprint of the value set a
// deployment to environment just carried, without changing the remembered
// source (an explicit --env-file leaves the memory's file alone). An empty
// fingerprint clears the record: a deployment with the stored values
// carried a set this checkout cannot hash. Without an existing entry
// nothing is written: a fingerprint alone is not a choice.
func SaveEnvFileFingerprint(root, environment, fingerprint string) error {
	if environment == "" {
		return nil
	}
	path := EnvFilesPath(root)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	unlock, err := filelock.Acquire(context.Background(), path+".lock")
	if err != nil {
		return err
	}
	defer unlock()
	environments, err := LoadEnvFiles(root)
	if err != nil {
		return err
	}
	choice, ok := environments[environment]
	if !ok {
		return nil
	}
	choice.Fingerprint = fingerprint
	environments[environment] = choice
	return writeEnvFiles(root, path, &envFilesState{SchemaVersion: 1, Environments: environments})
}

// writeEnvFiles replaces the memory file atomically: temp file, sync,
// rename, like the manifest review state.
func writeEnvFiles(root, path string, state *envFilesState) error {
	data, err := yaml.Marshal(state)
	if err != nil {
		return fmt.Errorf("checkout: marshal env file memory: %w", err)
	}
	f, err := os.CreateTemp(Dir(root), ".env-files-*")
	if err != nil {
		return fmt.Errorf("checkout: write %s: %w", path, err)
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err = f.Write(append([]byte(envFilesHeader), data...)); err != nil {
		return fmt.Errorf("checkout: write %s: %w", path, err)
	}
	if err = f.Sync(); err != nil {
		return fmt.Errorf("checkout: write %s: %w", path, err)
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("checkout: write %s: %w", path, err)
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return fmt.Errorf("checkout: write %s: %w", path, err)
	}
	return nil
}
