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
	"reflect"
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

// Paths lists every dotted path the document wrote, sorted. The manifest
// ledger matches its entries against it to learn what a manifest uses.
func (d *Document) Paths() []string {
	paths := make([]string, 0, len(d.locations))
	for path := range d.locations {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
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
		if unknown := unknownFields(root.Content[0], reflect.TypeOf(out), "", path); len(unknown) > 0 {
			return Document{}, unknown
		}
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

// UnknownFieldMessage is the message of every diagnostic Parse returns for
// a mapping key the decoded type has no field for. Callers that know more
// (the manifest's change ledger) rewrite it in place.
const UnknownFieldMessage = "unknown field"

var yamlUnmarshaler = reflect.TypeFor[yaml.Unmarshaler]()

// unknownFields walks the node tree next to the Go type it was decoded
// into and reports every mapping key the type has no field for, with the
// key's dotted path and position. yaml.v3 refuses such a document naming
// only the Go type and a line; the path is what an author, and the
// manifest's change ledger, act on. Types with their own UnmarshalYAML are
// leaves: their shape is theirs to judge.
func unknownFields(node *yaml.Node, t reflect.Type, path, file string) Diagnostics {
	var found Diagnostics
	walkUnknown(node, t, path, file, &found)
	return found
}

func walkUnknown(node *yaml.Node, t reflect.Type, path, file string, found *Diagnostics) {
	if node.Kind == yaml.AliasNode && node.Alias != nil {
		node = node.Alias
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Implements(yamlUnmarshaler) || reflect.PointerTo(t).Implements(yamlUnmarshaler) {
		return
	}
	switch node.Kind {
	case yaml.MappingNode:
		switch t.Kind() {
		case reflect.Struct:
			fields := yamlFields(t)
			for i := 0; i+1 < len(node.Content); i += 2 {
				key, value := node.Content[i], node.Content[i+1]
				child := joinPath(path, key.Value)
				fieldType, ok := fields[key.Value]
				if !ok {
					*found = append(*found, Diagnostic{
						File: file, Path: child, Line: key.Line, Column: key.Column, Message: UnknownFieldMessage,
					})
					continue
				}
				walkUnknown(value, fieldType, child, file, found)
			}
		case reflect.Map:
			for i := 0; i+1 < len(node.Content); i += 2 {
				walkUnknown(node.Content[i+1], t.Elem(), joinPath(path, node.Content[i].Value), file, found)
			}
		}
	case yaml.SequenceNode:
		if t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
			for i, child := range node.Content {
				walkUnknown(child, t.Elem(), fmt.Sprintf("%s[%d]", path, i), file, found)
			}
		}
	}
}

// yamlFields maps the keys a struct accepts to their field types the way
// yaml.v3 resolves them: the tag name, or the lowercased field name.
// Inline tags are not used by any skali document type.
func yamlFields(t reflect.Type) map[string]reflect.Type {
	fields := make(map[string]reflect.Type, t.NumField())
	for i := range t.NumField() {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}
		name := strings.ToLower(field.Name)
		if tag, ok := field.Tag.Lookup("yaml"); ok {
			tagName, _, _ := strings.Cut(tag, ",")
			if tagName == "-" {
				continue
			}
			if tagName != "" {
				name = tagName
			}
		}
		fields[name] = field.Type
	}
	return fields
}

func joinPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}
