// Command skali-schema generates the editor-facing JSON Schemas from the
// manifest and cluster-layout wire types (invoked through go generate), the
// release.json metadata the release workflow attaches to every release, and
// stamps pending manifest ledger entries with the release about to be cut.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"

	"github.com/Hinkolas/skali/internal/installer"
	"github.com/Hinkolas/skali/internal/layout"
	"github.com/Hinkolas/skali/internal/manifest"
	versionpkg "github.com/Hinkolas/skali/internal/version"
)

func main() {
	schema := flag.String("schema", "manifest", "schema to generate: manifest, layout, node, init, release, or ledger")
	output := flag.String("output", "", "output schema path; the ledger source file for --schema ledger")
	verify := flag.Bool("verify", false, "verify existing release metadata without writing")
	releaseVersion := flag.String("version", "", "release tag for --schema release and --schema ledger")
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
		data, err = releaseMetadata(*releaseVersion)
	case "ledger":
		// The ledger source is read and rewritten in place: every entry
		// written as Release: Next names the release being cut.
		if *releaseVersion == "" {
			fmt.Fprintln(os.Stderr, "error: --version is required for --schema ledger")
			os.Exit(2)
		}
		var source []byte
		source, err = os.ReadFile(*output)
		if err == nil {
			var stamped int
			data, stamped, err = stampLedger(source, *releaseVersion)
			if err == nil {
				fmt.Printf("stamped %d pending manifest ledger entries with %s in %s\n", stamped, *releaseVersion, *output)
			}
		}
	default:
		fmt.Fprintf(os.Stderr, "error: unknown --schema %q; expected manifest, layout, node, init, release, or ledger\n", *schema)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	if *verify {
		if *schema != "release" {
			fmt.Fprintln(os.Stderr, "--verify requires --schema release")
			os.Exit(2)
		}
		actual, err := os.ReadFile(*output)
		var got, want installer.ReleaseMetadata
		if err == nil {
			err = json.Unmarshal(actual, &got)
		}
		_ = json.Unmarshal(data, &want)
		if err != nil || got != want {
			fmt.Fprintln(os.Stderr, "release metadata does not match the release version and installer k3s pin")
			os.Exit(1)
		}
		return
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

func releaseMetadata(version string) ([]byte, error) {
	if err := ledgerWithin(manifest.Ledger, version); err != nil {
		return nil, err
	}
	return json.MarshalIndent(installer.ReleaseMetadata{Version: version, K3s: installer.K3sVersion}, "", "  ")
}

// ledgerWithin refuses to cut a release the manifest change ledger is not
// ready for: an entry still pending (Release: Next) has not been stamped
// with this release, and an entry naming a later release would describe a
// change as not yet shipped while the binaries next to this file already
// carry it. Snapshot rehearsals carry a goreleaser pseudo-version instead
// of a tag, so there is no release to measure against and nothing to
// refuse.
func ledgerWithin(ledger []manifest.Change, release string) error {
	if !versionpkg.IsRelease(release) {
		return nil
	}
	for _, change := range ledger {
		if change.Pending() {
			return fmt.Errorf("the manifest ledger entry for %s is still pending; stamp it with task release:stamp V=%s and merge that before tagging", change.Path, release)
		}
		if versionpkg.Older(release, change.Release) {
			return fmt.Errorf("the manifest ledger names %s but the release being cut is %s; fix internal/manifest/ledger.go", change.Release, release)
		}
	}
	return nil
}

// pendingEntry matches the Release field of a pending ledger entry in the
// ledger source, keeping the spacing gofmt chose.
var pendingEntry = regexp.MustCompile(`(\bRelease:\s*)Next,`)

// stampLedger rewrites the ledger source so every pending entry names
// release, which must be a release newer than every entry already stamped
// (the ledger stays oldest first). The rewrite is textual so comments and
// formatting survive; the result is parsed to make sure it is still Go.
func stampLedger(source []byte, release string) ([]byte, int, error) {
	if !versionpkg.IsRelease(release) {
		return nil, 0, fmt.Errorf("%q is not a release tag; expected a tag like v0.1.0 or v0.1.0-rc.1", release)
	}
	for _, change := range manifest.Ledger {
		if !change.Pending() && !versionpkg.Older(change.Release, release) {
			return nil, 0, fmt.Errorf("the manifest ledger already names %s, which is not older than %s; the ledger stays oldest first", change.Release, release)
		}
	}
	matches := pendingEntry.FindAllIndex(source, -1)
	if len(matches) == 0 {
		return source, 0, nil
	}
	stamped := pendingEntry.ReplaceAll(source, []byte("${1}"+fmt.Sprintf("%q", release)+","))
	if _, err := parser.ParseFile(token.NewFileSet(), "ledger.go", stamped, parser.SkipObjectResolution); err != nil {
		return nil, 0, fmt.Errorf("stamped ledger source does not parse: %w", err)
	}
	return stamped, len(matches), nil
}
