package skill_test

//go:generate go test . -run ^TestManifestChangeReferenceCurrent$ -update-manifest-changes

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/Hinkolas/skali/internal/manifest"
	"github.com/stretchr/testify/require"
)

var updateManifestChanges = flag.Bool("update-manifest-changes", false, "regenerate the manifest reference's change history")

func TestManifestChangeReferenceCurrent(t *testing.T) {
	const marker = "<!-- BEGIN GENERATED MANIFEST CHANGES -->"
	const path = "assets/reference/manifest.md"
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	prefix, _, _ := strings.Cut(string(data), marker)
	var out strings.Builder
	fmt.Fprintln(&out, marker)
	fmt.Fprintln(&out, "## Manifest change history")
	fmt.Fprintf(&out, "\nThis compiler understands manifest revision **%d**. Revision numbers order\n", manifest.CurrentRevision())
	fmt.Fprintln(&out, "change records; they are not release versions, deployment revisions, or the")
	fmt.Fprintln(&out, "`v1` in the floating editor-schema URL. Never put a revision in the manifest.")
	fmt.Fprintln(&out, "Run `skali skill read manifest --since N` for entries strictly after N;")
	fmt.Fprintln(&out, "`--since 0` prints the full ledger. `skali manifest upgrade` records local")
	fmt.Fprintln(&out, "review after validating; `--acknowledge` confirms relevant semantic changes,")
	fmt.Fprintln(&out, "not permission to use unsupported fields or skip compilation.")
	fmt.Fprintln(&out, "\nThe ledger began after the original grammar already existed, so it is not")
	fmt.Fprintln(&out, "an inventory of every feature. Revisions 7–10 backfill features that shipped")
	fmt.Fprintln(&out, "without records; they are informational additions, not changes introduced")
	fmt.Fprintln(&out, "by this release, and do not add acknowledgement gates. Old releases retain")
	fmt.Fprintln(&out, "their original embedded ledgers. The field reference above describes current")
	fmt.Fprintln(&out, "behavior; historical entries can describe fields that were later removed.")
	for _, change := range manifest.Ledger {
		fmt.Fprintf(&out, "\n### Revision %d: %s `%s`\n\n%s\n\n%s\n", change.Revision, change.Kind, change.Path, change.Message, change.Hint)
		if change.WhenOmitted {
			fmt.Fprintln(&out, "\nThis change affects manifests that omit the field, including omitted parent objects.")
		}
	}
	want := strings.TrimRight(prefix, "\n") + "\n\n" + out.String()
	if *updateManifestChanges {
		require.NoError(t, os.WriteFile(path, []byte(want), 0644))
		return
	}
	require.Equal(t, want, string(data), "manifest history drift: run go generate ./internal/skill")
}
