package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/Hinkolas/skali/internal/clirender"
)

// headerRow is one line of the summary a command prints before it acts: a
// dim label, the resolved value, and an optional dim note in parentheses.
type headerRow struct {
	label, value, note string
}

// headerLabelWidth aligns every summary on the widest standard label so the
// remote, project, and environment rows line up across commands, even when
// a command prints them at different points of its flow.
const headerLabelWidth = len("environment")

// printHeader writes the summary rows: what the command resolved and is
// about to act on, in the transcript's label column.
func printHeader(out io.Writer, style *clirender.Style, rows ...headerRow) {
	for _, row := range rows {
		line := style.Dim(row.label) + strings.Repeat(" ", max(headerLabelWidth-len(row.label), 0)+2) + row.value
		if row.note != "" {
			line += " " + style.Dim("("+row.note+")")
		}
		fmt.Fprintln(out, line)
	}
}
