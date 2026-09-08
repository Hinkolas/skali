// Command skali-schema generates the editor-facing JSON Schemas from the
// manifest and cluster-layout wire types (invoked through go generate), and
// the release.json metadata the release workflow attaches to every release.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Hinkolas/skali/internal/installer"
	"github.com/Hinkolas/skali/internal/layout"
	"github.com/Hinkolas/skali/internal/manifest"
)

func main() {
	schema := flag.String("schema", "manifest", "schema to generate: manifest, layout, node, init, or release")
	output := flag.String("output", "", "output schema path")
	releaseVersion := flag.String("version", "", "release tag for --schema release")
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
	case "release":
		// The k3s pin comes from the installer package of the build that
		// produces the release, so the file can never disagree with the
		// binaries next to it.
		if *releaseVersion == "" {
			fmt.Fprintln(os.Stderr, "error: --version is required for --schema release")
			os.Exit(2)
		}
		data, err = json.MarshalIndent(installer.ReleaseMetadata{
			Version: *releaseVersion, K3s: installer.K3sVersion,
		}, "", "  ")
	default:
		fmt.Fprintf(os.Stderr, "error: unknown --schema %q; expected manifest, layout, node, init, or release\n", *schema)
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
