package yamldoc

import (
	"strings"
	"testing"
)

func TestParseIndexesLocations(t *testing.T) {
	var out struct {
		Name  string            `yaml:"name"`
		Items map[string]string `yaml:"items"`
	}
	document, err := Parse([]byte("name: demo\nitems:\n  first: a\n"), "doc.yaml", "document", &out)
	if err != nil {
		t.Fatal(err)
	}
	if out.Name != "demo" {
		t.Errorf("decoded name = %q", out.Name)
	}
	if !document.Has("items.first") {
		t.Error("expected items.first to be indexed")
	}
	if position := document.Position("items.first"); position.Line != 3 {
		t.Errorf("items.first line = %d, want 3", position.Line)
	}
	if position := document.Position("items.first.missing.deeper"); position.Line != 3 {
		t.Errorf("ancestor fallback line = %d, want 3", position.Line)
	}
}

func TestParseRejections(t *testing.T) {
	var out struct{}
	if _, err := Parse(nil, "doc.yaml", "layout", &out); err == nil ||
		!strings.Contains(err.Error(), "layout is empty") {
		t.Errorf("empty document error = %v", err)
	}
	var open map[string]any
	if _, err := Parse([]byte("a: 1\n---\nb: 2\n"), "doc.yaml", "layout", &open); err == nil ||
		!strings.Contains(err.Error(), "multiple YAML documents") {
		t.Errorf("multi-document error = %v", err)
	}
}

func TestDiagnosticRendering(t *testing.T) {
	diagnostic := Diagnostic{File: "doc.yaml", Path: "a.b", Line: 4, Column: 2, Message: "bad"}
	if got := diagnostic.Error(); got != "doc.yaml:4:2: a.b: bad" {
		t.Errorf("Diagnostic.Error = %q", got)
	}
	diagnostics := Diagnostics{
		{File: "doc.yaml", Line: 9, Path: "z", Message: "later"},
		{File: "doc.yaml", Line: 2, Path: "a", Message: "earlier"},
	}
	rendered := diagnostics.Error()
	if !strings.HasPrefix(rendered, "doc.yaml:2:1: a: earlier\n") {
		t.Errorf("Diagnostics.Error not sorted by line: %q", rendered)
	}
	if Diagnostics(nil).Err() != nil {
		t.Error("empty Diagnostics must map to a nil error")
	}
}
