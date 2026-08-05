package layout

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/Hinkolas/skali/internal/yamldoc"
)

// Document is a parsed layout: the positioned YAML core plus the
// decoded layout.
type Document struct {
	yamldoc.Document
	Layout Layout
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
	var parsed Layout
	core, err := yamldoc.Parse(data, path, "layout", &parsed)
	if err != nil {
		return nil, err
	}
	return &Document{Document: core, Layout: parsed}, nil
}
