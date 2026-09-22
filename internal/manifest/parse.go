package manifest

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Hinkolas/skali/internal/yamldoc"
)

// Document is a parsed manifest: the positioned YAML core plus the
// decoded project and the root the manifest anchors relative paths to.
type Document struct {
	yamldoc.Document
	Project     Project
	ProjectRoot string
}

func ParseFile(path string) (*Document, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve manifest path: %w", err)
	}
	data, err := os.ReadFile(absolute)
	if err != nil {
		return nil, fmt.Errorf("read manifest %s: %w", absolute, err)
	}
	return Parse(data, absolute)
}

func Parse(data []byte, path string) (*Document, error) {
	var project Project
	core, err := yamldoc.Parse(data, path, "manifest", &project)
	if err != nil {
		return nil, explainUnknownFields(err)
	}
	return &Document{
		Document:    core,
		Project:     project,
		ProjectRoot: filepath.Dir(core.Path),
	}, nil
}

// explainUnknownFields explains removed fields without requiring review history.
func explainUnknownFields(err error) error {
	var diagnostics yamldoc.Diagnostics
	if !errors.As(err, &diagnostics) {
		return err
	}
	for i, diagnostic := range diagnostics {
		if diagnostic.Message != yamldoc.UnknownFieldMessage {
			continue
		}
		if change, found := Removed(diagnostic.Path); found {
			diagnostics[i].Message = fmt.Sprintf("removed in manifest revision %d: %s; %s", change.Revision, change.Message, change.Hint)
		}
	}
	return diagnostics
}

var ErrNotFound = errors.New("no skali.yml or skali.yaml found")

func Discover(explicit, start string) (string, error) {
	if explicit != "" {
		absolute, err := filepath.Abs(explicit)
		if err != nil {
			return "", fmt.Errorf("resolve --manifest: %w", err)
		}
		info, err := os.Stat(absolute)
		if err != nil {
			return "", fmt.Errorf("manifest %s: %w", absolute, err)
		}
		if info.IsDir() {
			return "", fmt.Errorf("manifest %s is a directory", absolute)
		}
		return absolute, nil
	}

	directory, err := filepath.Abs(start)
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}
	for {
		yml := filepath.Join(directory, "skali.yml")
		yamlPath := filepath.Join(directory, "skali.yaml")
		ymlExists := regularFile(yml)
		yamlExists := regularFile(yamlPath)
		switch {
		case ymlExists && yamlExists:
			return "", fmt.Errorf("both %s and %s exist; select one with --manifest", yml, yamlPath)
		case ymlExists:
			return yml, nil
		case yamlExists:
			return yamlPath, nil
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			break
		}
		directory = parent
	}
	return "", fmt.Errorf("%w from %s", ErrNotFound, start)
}

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
