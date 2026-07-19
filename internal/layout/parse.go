package layout

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
	Layout    Layout
	Path      string
	locations map[string]Position
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
		return nil, fmt.Errorf("resolve layout path: %w", err)
	}
	data, err := os.ReadFile(absolute)
	if err != nil {
		return nil, fmt.Errorf("read layout %s: %w", absolute, err)
	}
	return Parse(data, absolute)
}

func Parse(data []byte, path string) (*Document, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, syntaxDiagnostic(path, err)
	}
	if len(root.Content) == 0 {
		return nil, Diagnostic{File: path, Message: "layout is empty"}
	}

	var parsed Layout
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&parsed); err != nil {
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
		Layout:    parsed,
		Path:      absolute,
		locations: make(map[string]Position),
	}
	indexLocations(root.Content[0], "", document.locations)
	return document, nil
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
