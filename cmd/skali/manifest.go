package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/Hinkolas/skali/internal/checkout"
	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/manifest"
	"github.com/Hinkolas/skali/internal/yamldoc"
)

func newManifestCommand() *cobra.Command {
	command := &cobra.Command{Use: "manifest", Short: "Work on the project manifest"}
	command.AddCommand(newManifestUpgradeCommand())
	return command
}

func newManifestUpgradeCommand() *cobra.Command {
	var manifestPath string
	var acknowledge bool
	command := &cobra.Command{
		Use:   "upgrade",
		Short: "Clean up obsolete fields and update local manifest review history",
		Long:  "Validates safe manifest edits before writing. Relevant semantic changes require explicit review and --acknowledge. Review history is stored in .skali/, never in the manifest.",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			path, err := manifest.Discover(manifestPath, cwd)
			if err != nil {
				return err
			}
			reviewed, known, err := checkout.Review(path)
			if err != nil {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			info, err := os.Stat(path)
			if err != nil {
				return err
			}
			rewritten, summary, err := upgradeManifest(data)
			if err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			proposed := data
			if rewritten != nil {
				proposed = rewritten
			}
			document, err := manifest.Parse(proposed, path)
			if err != nil {
				return err
			}
			var acknowledged yamldoc.Diagnostics
			if known {
				diagnostics := manifest.ReviewChanges(document, reviewed)
				if len(diagnostics) > 0 {
					if !acknowledge {
						return diagnostics
					}
					acknowledged = diagnostics
				}
			}
			if _, err := compiler.Compile(document); err != nil {
				return err
			}
			if rewritten != nil {
				f, err := os.CreateTemp(filepath.Dir(path), ".skali-manifest-*")
				if err != nil {
					return err
				}
				defer os.Remove(f.Name())
				defer f.Close()
				if err = f.Chmod(info.Mode()); err != nil {
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
				// Do not replace a manifest edited while this command was preparing it.
				latest, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				if !bytes.Equal(latest, data) {
					return errors.New("manifest changed during upgrade; rerun the command")
				}
				if err = os.Rename(f.Name(), path); err != nil {
					return err
				}
			}
			stored, err := checkout.SaveReview(path, manifest.CurrentRevision(), false)
			if err != nil {
				if rewritten != nil {
					return fmt.Errorf("manifest cleanup succeeded, but local acknowledgement was not saved: %w; rerun manifest upgrade", err)
				}
				return fmt.Errorf("local acknowledgement was not saved: %w", err)
			}
			if len(acknowledged) > 0 {
				fmt.Fprintf(command.ErrOrStderr(), "acknowledged manifest changes:\n%s\n", acknowledged.Error())
			}
			fmt.Fprintf(command.OutOrStdout(), "upgraded %s: %s; locally reviewed through manifest revision %d\n", path, summary, stored)
			if stored > manifest.CurrentRevision() {
				fmt.Fprintf(command.ErrOrStderr(), "note: retaining newer local review revision %d; this compiler understands %d\n", stored, manifest.CurrentRevision())
			}
			return nil
		},
	}
	addVersionFlags(command, false)
	command.Flags().StringVar(&manifestPath, "manifest", "", "manifest path; defaults to skali.yml or skali.yaml")
	command.Flags().BoolVar(&acknowledge, "acknowledge", false, "confirm review of relevant semantic changes")
	return command
}

// upgradeManifest removes obsolete top-level fields without reformatting the
// remaining document. Unsafe YAML shapes require an explicit manual edit.
func upgradeManifest(data []byte) ([]byte, string, error) {
	var root yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&root); err != nil {
		return nil, "", err
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, "", errors.New("cannot safely rewrite multiple YAML documents")
	}
	if len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return nil, "", errors.New("manifest is not a mapping")
	}
	mapping := root.Content[0]
	var keys []*yaml.Node
	seen := map[string]bool{}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		key, value := mapping.Content[i], mapping.Content[i+1]
		if seen[key.Value] {
			return nil, "", fmt.Errorf("duplicate manifest field %q", key.Value)
		}
		seen[key.Value] = true
		if key.Value != "skali" && key.Value != "version" && key.Value != "backups" {
			continue
		}
		if mapping.Style&yaml.FlowStyle != 0 || value.Anchor != "" || value.Kind == yaml.AliasNode || value.Style&(yaml.FlowStyle|yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
			return nil, "", fmt.Errorf("cannot safely remove %s in this YAML shape; remove it explicitly", key.Value)
		}
		if key.Value != "backups" && (value.Kind != yaml.ScalarNode || key.Line != value.Line) {
			return nil, "", fmt.Errorf("cannot safely remove %s; remove it explicitly", key.Value)
		}
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return nil, "no manifest edits needed", nil
	}
	lines := strings.SplitAfter(string(data), "\n")
	// Compute all extents before changing lines; adjacent obsolete fields must
	// not affect the interpretation of one another's comments or blank lines.
	type extent struct {
		start, end  int
		replacement string
	}
	var removals []extent
	var labels []string
	for _, key := range keys {
		if !strings.HasPrefix(lines[key.Line-1], key.Value+":") {
			return nil, "", fmt.Errorf("cannot safely remove %s; remove it explicitly", key.Value)
		}
		if key.Value == "backups" {
			start, end := blockExtent(mapping, key, lines)
			removals = append(removals, extent{start: start, end: end})
			labels = append(labels, "removed backups (configure automatic backups with skali backup schedule set)")
		} else {
			// Preserve an inline comment as a standalone comment on the same line.
			value := mapping.Content[0]
			for i := 0; i+1 < len(mapping.Content); i += 2 {
				if mapping.Content[i] == key {
					value = mapping.Content[i+1]
					break
				}
			}
			ending := ""
			if strings.HasSuffix(lines[key.Line-1], "\r\n") {
				ending = "\r\n"
			} else if strings.HasSuffix(lines[key.Line-1], "\n") {
				ending = "\n"
			}
			replacement := ""
			if value.LineComment != "" {
				replacement = value.LineComment + ending
			}
			removals = append(removals, extent{start: key.Line - 1, end: key.Line, replacement: replacement})
			labels = append(labels, "removed "+key.Value)
		}
	}
	for _, removal := range removals {
		for i := removal.start; i < removal.end; i++ {
			lines[i] = ""
		}
		lines[removal.start] = removal.replacement
	}
	return []byte(strings.Join(lines, "")), strings.Join(labels, ", "), nil
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
