package checkout

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/Hinkolas/skali/internal/filelock"
	"gopkg.in/yaml.v3"
)

const reviewFilename = "manifest-review.yaml"

// reviewRevision rejects coercions such as YAML floats and numeric strings.
type reviewRevision int

func (r *reviewRevision) UnmarshalYAML(node *yaml.Node) error {
	if node.Tag != "!!int" {
		return errors.New("manifest review revision must be an integer")
	}
	var value int
	if err := node.Decode(&value); err != nil {
		return err
	}
	*r = reviewRevision(value)
	return nil
}

type reviewState struct {
	SchemaVersion int            `yaml:"schemaVersion"`
	Manifests     map[string]int `yaml:"manifests"`
}

func loadReviews(root string) (*reviewState, error) {
	path := filepath.Join(Dir(root), reviewFilename)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &reviewState{SchemaVersion: 1, Manifests: map[string]int{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read manifest review state %s: %w", path, err)
	}
	var raw struct {
		SchemaVersion reviewRevision             `yaml:"schemaVersion"`
		Manifests     map[string]*reviewRevision `yaml:"manifests"`
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	err = decoder.Decode(&raw)
	if err == nil {
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			err = errors.New("expected one YAML document")
		}
	}
	if err == nil && (raw.SchemaVersion != 1 || raw.Manifests == nil) {
		err = errors.New("expected schemaVersion: 1 and a manifests mapping")
	}
	state := reviewState{SchemaVersion: int(raw.SchemaVersion), Manifests: map[string]int{}}
	if err == nil {
		for name, revision := range raw.Manifests {
			if name == "" || name == "." || name == ".." || filepath.Base(name) != name || revision == nil || *revision < 0 {
				err = errors.New("manifest entries must use filenames and nonnegative revisions")
				break
			}
			state.Manifests[name] = int(*revision)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("invalid manifest review state %s: %w; repair it or delete it to reset local review history", path, err)
	}
	return &state, nil
}

// Review returns local acknowledgement for this manifest, without creating files.
func Review(manifestPath string) (revision int, known bool, err error) {
	absolute, err := filepath.Abs(manifestPath)
	if err != nil {
		return 0, false, err
	}
	state, err := loadReviews(filepath.Dir(absolute))
	if err != nil {
		return 0, false, err
	}
	revision, known = state.Manifests[filepath.Base(absolute)]
	return revision, known, nil
}

// SaveReview serializes updates and never lowers an existing acknowledgement.
// If onlyMissing is true, a concurrent first-use acknowledgement is preserved.
// The resulting stored revision is returned so callers can recheck a race.
func SaveReview(manifestPath string, revision int, onlyMissing bool) (int, error) {
	if revision < 0 {
		return 0, errors.New("manifest review revision cannot be negative")
	}
	absolute, err := filepath.Abs(manifestPath)
	if err != nil {
		return 0, err
	}
	root, name := filepath.Dir(absolute), filepath.Base(absolute)
	if err := EnsureDir(root); err != nil {
		return 0, err
	}
	path := filepath.Join(Dir(root), reviewFilename)
	unlock, err := filelock.Acquire(context.Background(), path+".lock")
	if err != nil {
		return 0, err
	}
	defer unlock()
	state, err := loadReviews(root)
	if err != nil {
		return 0, err
	}
	if previous, ok := state.Manifests[name]; ok && (onlyMissing || previous >= revision) {
		return previous, nil
	}
	state.Manifests[name] = revision
	data, err := yaml.Marshal(state)
	if err != nil {
		return 0, err
	}
	f, err := os.CreateTemp(Dir(root), ".manifest-review-*")
	if err != nil {
		return 0, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err = f.Write(data); err != nil {
		return 0, err
	}
	if err = f.Sync(); err != nil {
		return 0, err
	}
	if err = f.Close(); err != nil {
		return 0, err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return 0, err
	}
	return revision, nil
}
