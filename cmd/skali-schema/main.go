// Command skali-schema generates the editor-facing JSON Schema from the
// manifest wire types. It is invoked through go generate.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Hinkolas/skali/internal/manifest"
)

func main() {
	output := flag.String("output", "", "output schema path")
	flag.Parse()
	if *output == "" {
		fmt.Fprintln(os.Stderr, "error: --output is required")
		os.Exit(2)
	}
	data, err := manifest.JSONSchema()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(filepath.Dir(*output), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*output, data, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
