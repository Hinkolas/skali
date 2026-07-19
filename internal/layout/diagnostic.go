package layout

import (
	"fmt"
	"sort"
	"strings"
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
