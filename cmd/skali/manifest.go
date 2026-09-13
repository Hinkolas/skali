package main

import (
	"errors"
	"fmt"
	"os"
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
		Long: "Rewrites the skali field, the release the manifest was last reviewed " +
			"against, to this CLI's release (under dispatch, the cluster's) or to " +
			"--to, and replaces a legacy version field on the way. The manifest is " +
			"then compiled, so a change the new watermark acknowledges but the " +
			"manifest has not absorbed is reported with its migration hint.",
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
				fmt.Fprintf(out, "%s %s\n", path, summary)
				return nil
			}
			if err := os.WriteFile(path, rewritten, info.Mode().Perm()); err != nil {
				return fmt.Errorf("write manifest %s: %w", path, err)
			}
			fmt.Fprintf(out, "upgraded %s: %s\n", path, summary)
			document, err := manifest.Parse(rewritten, path)
			if err != nil {
				return err
			}
			_, err = compiler.Compile(document)
			return err
		},
	}
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
// Edits are line-based on the original text so formatting and comments
// elsewhere survive. A nil result means nothing changed; the summary then
// says why.
func upgradeManifest(data []byte, target string) (rewritten []byte, summary string, err error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, "", fmt.Errorf("parse manifest: %w", err)
	}
	if len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return nil, "", errors.New("manifest is not a mapping")
	}
	mapping := root.Content[0]
	var versionKey, versionValue, skaliKey, skaliValue *yaml.Node
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		switch mapping.Content[i].Value {
		case "version":
			versionKey, versionValue = mapping.Content[i], mapping.Content[i+1]
		case "skali":
			skaliKey, skaliValue = mapping.Content[i], mapping.Content[i+1]
		}
	}

	lines := strings.SplitAfter(string(data), "\n")
	replaceLine := func(key *yaml.Node) {
		index := key.Line - 1
		line := strings.TrimRight(lines[index], "\r\n")
		comment := ""
		if match := watermarkLine.FindStringSubmatch(line); match != nil {
			comment = match[1]
		}
		lines[index] = "skali: " + target + comment + "\n"
	}

	switch {
	case skaliKey != nil:
		current, ok := manifest.Watermark(skaliValue.Value)
		if ok && current == target {
			return nil, fmt.Sprintf("is already reviewed against %s", target), nil
		}
		if ok && versionpkg.Older(target, current) {
			return nil, fmt.Sprintf("is reviewed against %s, newer than %s; nothing to do", current, target), nil
		}
		replaceLine(skaliKey)
		summary = fmt.Sprintf("skali %s -> %s", skaliValue.Value, target)
		if versionKey != nil {
			lines[versionKey.Line-1] = ""
			summary = fmt.Sprintf("removed version %q, %s", versionValue.Value, summary)
		}
	case versionKey != nil:
		replaceLine(versionKey)
		summary = fmt.Sprintf("version %q -> skali: %s", versionValue.Value, target)
	default:
		first := 0
		if len(mapping.Content) > 0 {
			first = mapping.Content[0].Line - 1
		}
		lines = append(lines[:first], append([]string{"skali: " + target + "\n"}, lines[first:]...)...)
		summary = "added skali: " + target
	}
	return []byte(strings.Join(lines, "")), summary, nil
}
