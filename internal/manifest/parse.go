package manifest

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/Hinkolas/skali/internal/version"
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
		return nil, explainUnknownFields(data, err)
	}
	return &Document{
		Document:    core,
		Project:     project,
		ProjectRoot: filepath.Dir(core.Path),
	}, nil
}

// explainUnknownFields turns the parser's unknown-field diagnostics into
// what the author can act on: a field the ledger removed names its
// replacement, and a field this release has never seen is told apart from
// a typo when the watermark says the manifest targets a newer release.
func explainUnknownFields(data []byte, err error) error {
	var diagnostics yamldoc.Diagnostics
	if !errors.As(err, &diagnostics) {
		return err
	}
	var head struct {
		Skali string `yaml:"skali"`
	}
	_ = yaml.Unmarshal(data, &head)
	watermark, ok := Watermark(head.Skali)
	newer := ok && version.IsRelease(version.Version) && version.Older(version.Version, watermark)
	for i, diagnostic := range diagnostics {
		if diagnostic.Message != yamldoc.UnknownFieldMessage {
			continue
		}
		if change, found := Removed(diagnostic.Path); found {
			diagnostics[i].Message = fmt.Sprintf("removed in %s: %s; %s", change.ReleaseLabel(), change.Message, change.Hint)
		} else if newer {
			diagnostics[i].Message = fmt.Sprintf("unknown field; the manifest was reviewed against %s, newer than this CLI (%s)",
				watermark, version.Version)
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
