// Package yamldoc carries the shared machinery of positioned YAML
// documents: a strict two-pass parse, a location index over the node
// tree, and diagnostics that render as file:line:column errors. The
// manifest and layout packages embed Document and add only their own
// decoded content.
package yamldoc

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type Position struct {
	Line   int
	Column int
}

type Diagnostic struct {
	File    string
	Path    string
	Line    int
	Column  int
	Message string
}

func (d Diagnostic) Error() string {
	location := d.File
	if d.Line > 0 {
		location = fmt.Sprintf("%s:%d:%d", location, d.Line, max(d.Column, 1))
	}
	if d.Path != "" {
		return fmt.Sprintf("%s: %s: %s", location, d.Path, d.Message)
	}
	return fmt.Sprintf("%s: %s", location, d.Message)
}

type Diagnostics []Diagnostic

func (d Diagnostics) Error() string {
	if len(d) == 0 {
		return ""
	}
	items := append(Diagnostics(nil), d...)
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Line != items[j].Line {
			return items[i].Line < items[j].Line
		}
		return items[i].Path < items[j].Path
	})
	lines := make([]string, len(items))
	for i, item := range items {
		lines[i] = item.Error()
	}
	return strings.Join(lines, "\n")
}

func (d Diagnostics) Err() error {
	if len(d) == 0 {
		return nil
	}
	return d
}

// Document is the positioned core of one parsed YAML file: its absolute
// path and the dotted-path location index diagnostics anchor to.
type Document struct {
	Path      string
	locations map[string]Position
}

// Position resolves a dotted path to its source position, walking up to
// the nearest indexed ancestor when the exact path was never written.
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

// Parse strictly decodes exactly one YAML document into out and returns
// the positioned core. The noun names the document kind in diagnostics
// ("manifest is empty"). Errors are Diagnostic values.
func Parse(data []byte, path, noun string, out any) (Document, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return Document{}, syntaxDiagnostic(path, err)
	}
	if len(root.Content) == 0 {
		return Document{}, Diagnostic{File: path, Message: noun + " is empty"}
	}

	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(out); err != nil {
		return Document{}, syntaxDiagnostic(path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Document{}, Diagnostic{File: path, Message: "multiple YAML documents are not supported"}
		}
		return Document{}, syntaxDiagnostic(path, err)
	}

	absolute, err := filepath.Abs(path)
	if err != nil {
		absolute = path
	}
	document := Document{Path: absolute, locations: make(map[string]Position)}
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
