package manifest

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type Document struct {
	Project     Project
	Path        string
	ProjectRoot string
	locations   map[string]Position
}

func (d *Document) Position(path string) Position {
	for {
		if position, ok := d.locations[path]; ok {
			return position
		}
		index := strings.LastIndexByte(path, '.')
		if index < 0 {
			return Position{}
		}
		path = path[:index]
	}
}

func (d *Document) Has(path string) bool {
	_, ok := d.locations[path]
	return ok
}

func (d *Document) Diagnostic(path, message string) Diagnostic {
	position := d.Position(path)
	return Diagnostic{
		File:    d.Path,
		Path:    path,
		Line:    position.Line,
		Column:  position.Column,
		Message: message,
	}
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
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, syntaxDiagnostic(path, err)
	}
	if len(root.Content) == 0 {
		return nil, Diagnostic{File: path, Message: "manifest is empty"}
	}

	var project Project
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&project); err != nil {
		return nil, syntaxDiagnostic(path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, Diagnostic{File: path, Message: "multiple YAML documents are not supported"}
		}
		return nil, syntaxDiagnostic(path, err)
	}

	absolute, err := filepath.Abs(path)
	if err != nil {
		absolute = path
	}
	document := &Document{
		Project:     project,
		Path:        absolute,
		ProjectRoot: filepath.Dir(absolute),
		locations:   make(map[string]Position),
	}
	indexLocations(root.Content[0], "", document.locations)
	return document, nil
}

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
	return "", fmt.Errorf("no skali.yml or skali.yaml found from %s", start)
}

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func indexLocations(node *yaml.Node, path string, locations map[string]Position) {
	if path != "" {
		locations[path] = Position{Line: node.Line, Column: node.Column}
	}
	switch node.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			key := node.Content[i]
			value := node.Content[i+1]
			child := key.Value
			if path != "" {
				child = path + "." + child
			}
			locations[child] = Position{Line: value.Line, Column: value.Column}
			indexLocations(value, child, locations)
		}
	case yaml.SequenceNode:
		for i, child := range node.Content {
			indexLocations(child, fmt.Sprintf("%s[%d]", path, i), locations)
		}
	}
}

var linePattern = regexp.MustCompile("line ([0-9]+)")

func syntaxDiagnostic(path string, err error) error {
	message := strings.TrimPrefix(err.Error(), "yaml: ")
	line := 0
	if match := linePattern.FindStringSubmatch(message); len(match) == 2 {
		line, _ = strconv.Atoi(match[1])
	}
	message = strings.ReplaceAll(message, "unmarshal errors:\n  ", "")
	return Diagnostic{File: path, Line: line, Column: 1, Message: message}
}
