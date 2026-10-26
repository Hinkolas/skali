package main

//go:generate go test . -run ^TestCLIReferenceCurrent$ -update-cli-reference

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"
)

var updateCLIReference = flag.Bool("update-cli-reference", false, "regenerate the command catalog in the embedded CLI reference")

const cliCatalogMarker = "<!-- BEGIN GENERATED COMMAND CATALOG -->"

// Walk the real command tree, including inherited flags, without executing
// hooks, completion functions or commands. The shipped reference must describe
// the same interface as this binary; hand-written workflow advice precedes it.
func TestCLIReferenceCurrent(t *testing.T) {
	path := filepath.Join("..", "..", "internal", "skill", "assets", "reference", "cli.md")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	prefix, _, _ := strings.Cut(string(data), cliCatalogMarker)
	want := strings.TrimRight(prefix, "\n") + "\n\n" + cliCommandCatalog()
	if *updateCLIReference {
		require.NoError(t, os.WriteFile(path, []byte(want), 0644))
		return
	}
	require.Equal(t, want, string(data), "CLI reference drift: run go generate ./cmd/skali and review the command/flag descriptions")
}

func cliCommandCatalog() string {
	root := newRootCommand()
	root.InitDefaultHelpCmd()
	root.InitDefaultVersionFlag()
	var commands []*cobra.Command
	var walk func(*cobra.Command)
	walk = func(command *cobra.Command) {
		if command.Hidden {
			return
		}
		command.InitDefaultHelpFlag()
		commands = append(commands, command)
		for _, child := range command.Commands() {
			walk(child)
		}
	}
	walk(root)
	var out strings.Builder
	fmt.Fprintln(&out, cliCatalogMarker)
	fmt.Fprintln(&out, "## Complete command and flag reference")
	fmt.Fprintln(&out, "\nThis catalog is generated from this release's command definitions. Each entry")
	fmt.Fprintln(&out, "includes its synopsis, aliases, help, and every public local or inherited flag.")
	fmt.Fprintln(&out, "An empty string (`\"\"`) means no explicit value; the description explains any")
	fmt.Fprintln(&out, "runtime default. `stringSlice` accepts comma-separated values or repeated flags;")
	fmt.Fprintln(&out, "`stringArray` takes one value per occurrence (repeat the flag for more). Boolean")
	fmt.Fprintln(&out, "flags can be written alone for true or as `--flag=false`. `duration` flags use")
	fmt.Fprintln(&out, "Go duration syntax (for example `1h30m`); string flags such as `--keep` and")
	fmt.Fprintln(&out, "`--retire-after` use the documented Skali units, which also include `d` and `w`.")
	fmt.Fprintln(&out, "Only the flags listed for a command apply to it; `--remote`, `--environment`,")
	fmt.Fprintln(&out, "`--manifest`, `--offline`, and `--yes` are not universal flags. `--` ends Skali")
	fmt.Fprintln(&out, "flag parsing before an exec or host command's arguments.")
	fmt.Fprint(&out, "\n### Command index\n\n")
	for _, command := range commands {
		fmt.Fprintf(&out, "- [%s](#%s): %s\n", command.CommandPath(), cliCommandAnchor(command), command.Short)
	}
	for _, command := range commands {
		fmt.Fprintf(&out, "\n### `%s`\n\n```text\n%s\n```\n", command.CommandPath(), command.UseLine())
		if len(command.Aliases) > 0 {
			fmt.Fprintf(&out, "\nAliases: `%s`.\n", strings.Join(command.Aliases, "`, `"))
		}
		description := command.Long
		if description == "" {
			description = command.Short
		}
		fmt.Fprintf(&out, "\n%s\n", strings.TrimSpace(description))
		if command.Deprecated != "" {
			fmt.Fprintf(&out, "\nDeprecated: %s\n", command.Deprecated)
		}
		if command.Example != "" {
			fmt.Fprintf(&out, "\n```sh\n%s\n```\n", strings.TrimSpace(command.Example))
		}
		fmt.Fprintln(&out, "\n| Flag | Type | Default | Meaning |")
		fmt.Fprintln(&out, "| --- | --- | --- | --- |")
		flags := pflag.NewFlagSet(command.Name(), pflag.ContinueOnError)
		flags.AddFlagSet(command.LocalFlags())
		flags.AddFlagSet(command.InheritedFlags())
		flags.VisitAll(func(f *pflag.Flag) {
			if f.Hidden {
				return
			}
			name := "`--" + f.Name + "`"
			if f.Shorthand != "" && f.ShorthandDeprecated == "" {
				name += ", `-" + f.Shorthand + "`"
			}
			def := f.DefValue
			if def == "" {
				def = `""`
			}
			meaning := f.Usage
			if command.InheritedFlags().Lookup(f.Name) != nil {
				meaning += " (inherited)"
			}
			if len(f.Annotations[cobra.BashCompOneRequiredFlag]) > 0 {
				meaning += " **Required.**"
			}
			if f.Deprecated != "" {
				meaning += " Deprecated: " + f.Deprecated
			}
			fmt.Fprintf(&out, "| %s | `%s` | `%s` | %s |\n", name, f.Value.Type(), cliTableCell(def), cliTableCell(meaning))
		})
		// Cobra's declarative flag groups are part of the interface too.
		var rules []string
		seen := map[string]bool{}
		flags.VisitAll(func(f *pflag.Flag) {
			for annotation, label := range map[string]string{
				"cobra_annotation_mutually_exclusive": "Mutually exclusive",
				"cobra_annotation_required_together":  "Required together",
				"cobra_annotation_one_required":       "At least one required",
			} {
				for _, group := range f.Annotations[annotation] {
					rule := label + ": `--" + strings.ReplaceAll(group, " ", "`, `--") + "`."
					if !seen[rule] {
						seen[rule] = true
						rules = append(rules, rule)
					}
				}
			}
		})
		sort.Strings(rules)
		for _, rule := range rules {
			fmt.Fprintf(&out, "\n%s\n", rule)
		}
	}
	return out.String()
}

func cliCommandAnchor(command *cobra.Command) string {
	return strings.ReplaceAll(command.CommandPath(), " ", "-")
}

func cliTableCell(s string) string {
	return strings.NewReplacer("|", "\\|", "\n", "<br>").Replace(s)
}
