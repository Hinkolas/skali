// Command skali-schema generates the editor-facing JSON Schemas from the
// manifest and cluster-layout wire types. It is invoked through go generate.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Hinkolas/skali/internal/installer"
	"github.com/Hinkolas/skali/internal/layout"
	"github.com/Hinkolas/skali/internal/manifest"
)

func main() {
	schema := flag.String("schema", "manifest", "schema to generate: manifest, layout, node, init, or existing-cluster")
	output := flag.String("output", "", "output schema path")
	flag.Parse()
	if *output == "" {
		fmt.Fprintln(os.Stderr, "error: --output is required")
		os.Exit(2)
	}

	var data []byte
	var err error
	switch *schema {
	case "manifest":
		data, err = manifest.JSONSchema()
	case "layout":
		data, err = layout.JSONSchema()
	case "node":
		data, err = installer.NodeConfigJSONSchema()
	case "init":
		data, err = installer.InitConfigJSONSchema()
	case "existing-cluster":
		data, err = installer.ExistingClusterConfigJSONSchema()
	default:
		fmt.Fprintf(os.Stderr, "error: unknown --schema %q; expected manifest, layout, node, init, or existing-cluster\n", *schema)
		os.Exit(2)
	}
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
