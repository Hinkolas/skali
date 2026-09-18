package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/manifest"
	versionpkg "github.com/Hinkolas/skali/internal/version"
)

func newManifestCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "manifest",
		Short: "Work on the project manifest",
	}
	command.AddCommand(newManifestUpgradeCommand())
	return command
}

// newManifestUpgradeCommand moves the watermark (docs/versioning.md,
// decision 4). The command dispatches like every other, so under a binding
// or current remote the release it writes is the cluster's.
func newManifestUpgradeCommand() *cobra.Command {
	var (
		manifestPath string
		to           string
	)
	command := &cobra.Command{
		Use:   "upgrade",
		Short: "Move the manifest's skali watermark to this release",
		Long: "Validates a proposed review-point advance and safe mechanical changes before " +
			"writing the manifest. Semantic changes require author review and an explicit " +
			"watermark edit; errors leave the file unchanged. A released CLI can certify " +
			"only its own release. Working-tree builds require --to and identify their " +
			"development status.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			target, err := watermarkTarget(to)
			if err != nil {
				return err
			}
			workingDirectory, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get working directory: %w", err)
			}
			path, err := manifest.Discover(manifestPath, workingDirectory)
			if err != nil {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("read manifest %s: %w", path, err)
			}
			info, err := os.Stat(path)
			if err != nil {
				return fmt.Errorf("stat manifest %s: %w", path, err)
			}
			rewritten, summary, err := upgradeManifest(data, target)
			if err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			out := command.OutOrStdout()
			if rewritten == nil {
				doc, err := manifest.Parse(data, path)
				if err != nil {
					return err
				}
				if _, err := compiler.Compile(doc); err != nil {
					return err
				}
				fmt.Fprintf(out, "%s %s\n", path, summary)
				return nil
			}
			document, err := manifest.Parse(rewritten, path)
			if err != nil {
				return err
			}
			var old struct {
				Skali string `yaml:"skali"`
			}
			if err := yaml.Unmarshal(data, &old); err != nil {
				return err
			}
			reviewed, ok := manifest.Watermark(old.Skali)
			if !ok {
				reviewed = "v0.0.0"
			}
			if diagnostics := manifest.ReviewChanges(document, reviewed); len(diagnostics) > 0 {
				return diagnostics
			}
			if _, err = compiler.Compile(document); err != nil {
				return err
			}
			f, err := os.CreateTemp(filepath.Dir(path), ".skali-manifest-*")
			if err != nil {
				return err
			}
			defer os.Remove(f.Name())
			defer f.Close()
			if err = f.Chmod(info.Mode().Perm()); err != nil {
				return err
			}
			if _, err = f.Write(rewritten); err != nil {
				return err
			}
			if err = f.Sync(); err != nil {
				return err
			}
			if err = f.Close(); err != nil {
				return err
			}
			if err = os.Rename(f.Name(), path); err != nil {
				return err
			}
			fmt.Fprintf(out, "upgraded %s: %s\n", path, summary)
			if !versionpkg.IsRelease(versionpkg.Version) {
				fmt.Fprintln(out, "reviewed with working-tree compiler "+versionpkg.Version)
			}
			return nil
		},
	}
	addVersionFlags(command, false)
	command.Flags().StringVar(&manifestPath, "manifest", "", "manifest path; defaults to skali.yml or skali.yaml")
	command.Flags().StringVar(&to, "to", "", "release to review against; defaults to this CLI's release")
	return command
}

// watermarkTarget resolves the release the watermark moves to. A development
// build names no release and needs --to.
func watermarkTarget(to string) (string, error) {
	if to != "" {
		release, ok := manifest.Watermark(to)
		if !ok {
			return "", fmt.Errorf("--to %q is not a skali release; expected a tag like %s", to, manifest.ReferenceRelease())
		}
		if versionpkg.IsRelease(versionpkg.Version) && release != versionpkg.Version {
			return "", fmt.Errorf("--to %s differs from this compiler (%s); select the matching target instead", release, versionpkg.Version)
		}
		return release, nil
	}
	if !versionpkg.IsRelease(versionpkg.Version) {
		return "", errors.New("this is a development build and names no release; pass --to <release>")
	}
	return versionpkg.Version, nil
}

// watermarkLine matches a top-level key line and keeps an inline comment.
var watermarkLine = regexp.MustCompile(`^[A-Za-z_]+:[^#]*?(\s*#.*)?$`)

// upgradeManifest rewrites the manifest text so its watermark is target:
// the legacy version line becomes the skali line, an older skali line moves
// forward, and a manifest with neither gets the line before its first key.
// A top-level backups block (removed in v0.1.0-rc.8, schedules are an
// environment setting now) is dropped with the comment block directly
// above it. Edits are line-based on the original text so formatting and
// comments elsewhere survive. A nil result means nothing changed; the
// summary then says why.
func upgradeManifest(data []byte, target string) (rewritten []byte, summary string, err error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, "", fmt.Errorf("parse manifest: %w", err)
	}
	if len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return nil, "", errors.New("manifest is not a mapping")
	}
	mapping := root.Content[0]
	if mapping.Style&yaml.FlowStyle != 0 {
		return nil, "", errors.New("cannot safely rewrite a flow-style manifest; edit the watermark explicitly")
	}
	var versionKey, versionValue, skaliKey, skaliValue, backupsKey, backupsValue *yaml.Node
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		switch mapping.Content[i].Value {
		case "version":
			versionKey, versionValue = mapping.Content[i], mapping.Content[i+1]
		case "skali":
			skaliKey, skaliValue = mapping.Content[i], mapping.Content[i+1]
		case "backups":
			backupsKey, backupsValue = mapping.Content[i], mapping.Content[i+1]
		}
	}

	lines := strings.SplitAfter(string(data), "\n")
	newline := "\n"
	if bytes.Contains(data, []byte("\r\n")) {
		newline = "\r\n"
	}
	var removals []string
	if backupsKey != nil {
		if backupsValue.Anchor != "" || backupsValue.Kind == yaml.AliasNode || backupsValue.Style&yaml.FlowStyle != 0 ||
			!strings.HasPrefix(lines[backupsKey.Line-1], "backups:") {
			return nil, "", errors.New("cannot safely remove this backups block; delete it explicitly")
		}
		start, end := blockExtent(mapping, backupsKey, lines)
		for i := start; i < end; i++ {
			lines[i] = ""
		}
		removals = append(removals, "removed backups (automatic backups are an environment setting now: skali backup schedule set)")
	}
	for _, pair := range [][2]*yaml.Node{{versionKey, versionValue}, {skaliKey, skaliValue}} {
		key, value := pair[0], pair[1]
		if key == nil {
			continue
		}
		line := strings.TrimRight(lines[key.Line-1], "\r\n")
		if value.Anchor != "" || value.Kind != yaml.ScalarNode || value.Line != key.Line || value.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 || !strings.HasPrefix(line, key.Value+":") {
			return nil, "", errors.New("cannot safely rewrite this watermark shape; edit it explicitly")
		}
	}
	replaceLine := func(key *yaml.Node) {
		index := key.Line - 1
		line := strings.TrimRight(lines[index], "\r\n")
		comment := ""
		if match := watermarkLine.FindStringSubmatch(line); match != nil {
			comment = match[1]
		}
		ending := ""
		if strings.HasSuffix(lines[index], "\n") {
			ending = newline
		}
		lines[index] = "skali: " + target + comment + ending
	}

	switch {
	case skaliKey != nil:
		current, ok := manifest.Watermark(skaliValue.Value)
		switch {
		case ok && current == target:
			summary = fmt.Sprintf("is already reviewed against %s", target)
		case ok && versionpkg.Older(target, current):
			summary = fmt.Sprintf("is reviewed against %s, newer than %s; nothing to do", current, target)
		default:
			replaceLine(skaliKey)
			summary = fmt.Sprintf("skali %s -> %s", skaliValue.Value, target)
			if versionKey != nil {
				lines[versionKey.Line-1] = ""
				summary = fmt.Sprintf("removed version %q, %s", versionValue.Value, summary)
			}
		}
		if ok && !versionpkg.Older(current, target) {
			// The watermark needs no move; only a removal makes an edit.
			if len(removals) == 0 {
				return nil, summary, nil
			}
			summary = strings.Join(removals, ", ")
			return []byte(strings.Join(lines, "")), summary, nil
		}
	case versionKey != nil:
		replaceLine(versionKey)
		summary = fmt.Sprintf("version %q -> skali: %s", versionValue.Value, target)
	default:
		first := 0
		if len(mapping.Content) > 0 {
			first = mapping.Content[0].Line - 1
		}
		lines = append(lines[:first], append([]string{"skali: " + target + newline}, lines[first:]...)...)
		summary = "added skali: " + target
	}
	if len(removals) > 0 {
		summary = strings.Join(append(removals, summary), ", ")
	}
	return []byte(strings.Join(lines, "")), summary, nil
}

// blockExtent is the half-open line range [start, end) a top-level key
// occupies: its line through the line before the next top-level key (or
// the end of the file), pulling in the full-line comment block directly
// above it. Trailing blank lines are trimmed so exactly one blank line
// separates the neighbours afterwards, and a column-1 comment run that sits
// after a blank line right above the next key belongs to that key and is
// left alone.
func blockExtent(mapping *yaml.Node, key *yaml.Node, lines []string) (start, end int) {
	start = key.Line - 1
	end = len(lines)
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if l := mapping.Content[i].Line - 1; l > start && l < end {
			end = l
		}
	}
	blank := func(i int) bool { return strings.TrimSpace(lines[i]) == "" }
	comment := func(i int) bool { return strings.HasPrefix(lines[i], "#") }
	// A comment run leading straight into the next key is that key's.
	if end < len(lines) {
		i := end
		for i > start+1 && comment(i-1) {
			i--
		}
		if i < end && i > start+1 && blank(i-1) {
			end = i
		}
	}
	for end > start+1 && blank(end-1) {
		end--
	}
	for start > 0 && comment(start-1) {
		start--
	}
	if start > 0 && blank(start-1) {
		for end < len(lines) && blank(end) {
			end++
		}
	}
	// A block that closes the file takes the blank lines above it along.
	if end == len(lines) {
		for start > 0 && blank(start-1) {
			start--
		}
	}
	return start, end
}
