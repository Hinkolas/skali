package main

//go:generate go test . -run ^TestCLIReferenceCurrent$ -update-cli-reference

import (
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"
)

var updateCLIReference = flag.Bool("update-cli-reference", false, "regenerate the CLI overview and focused command pages")

const cliCatalogMarker = "<!-- BEGIN GENERATED COMMAND CATALOG -->"
const cliPageMarker = "<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->"

var cliWorkflows = []struct{ name, title string }{
	{"deployments", "Deployments, values, and stored revisions"},
	{"runs", "Logs and runs"},
	{"setup", "Remotes, CLI installation, and completions"},
	{"cluster", "Cluster installation and maintenance"},
	{"development", "Local development"},
	{"backups", "Backups"},
	{"buckets", "Buckets"},
	{"databases", "Databases, SQL, and container exec"},
	{"routes", "Routes"},
	{"access", "Access and environments"},
}

// Walk the actual public command tree, including inherited flags, without
// executing commands. No command or flag can disappear when the docs split.
func TestCLIReferenceCurrent(t *testing.T) {
	directory := filepath.Join("..", "..", "internal", "skill", "assets", "reference")
	data, err := os.ReadFile(filepath.Join(directory, "cli.md"))
	require.NoError(t, err)
	prefix, _, _ := strings.Cut(string(data), cliCatalogMarker)
	want := cliCommandReferences()
	want["cli.md"] = strings.TrimRight(prefix, "\n") + "\n\n" + want["cli.md"]
	for _, workflow := range cliWorkflows {
		require.FileExists(t, filepath.Join(directory, "cli", "workflows", workflow.name+".md"))
	}
	// Retired command pages must disappear too. Only generated pages are pruned;
	// workflow prose is maintained separately and must never be overwritten.
	err = fs.WalkDir(os.DirFS(directory), "cli", func(path string, entry fs.DirEntry, err error) error {
		require.NoError(t, err)
		if entry.IsDir() || !strings.HasSuffix(path, ".md") || strings.HasPrefix(path, "cli/workflows/") {
			return nil
		}
		if _, exists := want[path]; !exists {
			data, err := os.ReadFile(filepath.Join(directory, filepath.FromSlash(path)))
			require.NoError(t, err)
			if *updateCLIReference && strings.Contains(string(data), cliPageMarker) {
				require.NoError(t, os.Remove(filepath.Join(directory, filepath.FromSlash(path))))
			} else {
				t.Errorf("unexpected CLI reference %s: remove retired pages or move workflow prose under cli/workflows/", path)
			}
		}
		return nil
	})
	require.NoError(t, err)
	var paths []string
	for path := range want {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		target := filepath.Join(directory, filepath.FromSlash(path))
		if *updateCLIReference {
			require.NoError(t, os.MkdirAll(filepath.Dir(target), 0755))
			require.NoError(t, os.WriteFile(target, []byte(want[path]), 0644))
			continue
		}
		data, err := os.ReadFile(target)
		require.NoError(t, err, "run go generate ./cmd/skali")
		require.Equal(t, want[path], string(data), "%s drift: run go generate ./cmd/skali and review descriptions", path)
	}
}

func cliCommandReferences() map[string]string {
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
	files := map[string]string{}
	var overview strings.Builder
	fmt.Fprintln(&overview, cliCatalogMarker)
	fmt.Fprintln(&overview, "## Workflow guides")
	fmt.Fprintln(&overview, "\nRead the relevant guide for behavior spanning multiple commands. Each command")
	fmt.Fprintln(&overview, "page links to its guide. `skali skill read cli workflows` lists these guides.")
	var workflows strings.Builder
	fmt.Fprintf(&workflows, "# CLI workflow guides\n\n%s\n\n[CLI overview](../cli.md). Read only the guide relevant to the task.\n", cliPageMarker)
	for _, workflow := range cliWorkflows {
		fmt.Fprintf(&overview, "\n- [%s](cli/workflows/%s.md): `skali skill read cli workflows %s`", workflow.title, workflow.name, workflow.name)
		fmt.Fprintf(&workflows, "\n- [%s](workflows/%s.md): `skali skill read cli workflows %s`", workflow.title, workflow.name, workflow.name)
	}
	fmt.Fprintln(&workflows)
	files["cli/workflows.md"] = workflows.String()
	fmt.Fprint(&overview, "\n\n## Global invocation and flags\n\n")
	cliCommandEntry(&overview, root)
	fmt.Fprintln(&overview, "\n## Command index")
	fmt.Fprintln(&overview, "\nFor `skali A B`, read `skali skill read cli A B`. All public commands are listed;")
	fmt.Fprint(&overview, "each link opens only that command's reference. Global flags are above.\n\n")
	for _, command := range commands[1:] {
		name := strings.TrimPrefix(command.CommandPath(), root.Name()+" ")
		path := "cli/" + strings.ReplaceAll(name, " ", "/") + ".md"
		fmt.Fprintf(&overview, "- [`%s`](%s): %s\n", command.CommandPath(), path, command.Short)
		var page strings.Builder
		fmt.Fprintf(&page, "# %s\n\n%s\n\nRead with `skali skill read cli %s`.\n", command.CommandPath(), cliPageMarker, name)
		link := func(target string) string {
			relative, err := filepath.Rel(filepath.Dir(filepath.FromSlash(path)), filepath.FromSlash(target))
			if err != nil {
				panic(err)
			}
			return filepath.ToSlash(relative)
		}
		fmt.Fprintf(&page, "[CLI overview](%s). ", link("cli.md"))
		workflow := cliCommandWorkflow(name)
		fmt.Fprintf(&page, "[Workflow guide](%s): `skali skill read cli workflows %s`.\n\n", link("cli/workflows/"+workflow+".md"), workflow)
		cliCommandEntry(&page, command)
		children := false
		for _, child := range command.Commands() {
			if child.Hidden {
				continue
			}
			if !children {
				fmt.Fprint(&page, "\n## Subcommands\n\n")
				children = true
			}
			childName := strings.TrimPrefix(child.CommandPath(), root.Name()+" ")
			childPath := "cli/" + strings.ReplaceAll(childName, " ", "/") + ".md"
			fmt.Fprintf(&page, "- [`%s`](%s): %s\n", child.CommandPath(), link(childPath), child.Short)
		}
		files[path] = page.String()
	}
	files["cli.md"] = overview.String()
	return files
}

func cliCommandWorkflow(name string) string {
	// SQL and container sessions share semantics across local and remote modes.
	if name == "dev sql" || name == "dev exec" {
		return "databases"
	}
	first, _, _ := strings.Cut(name, " ")
	switch first {
	case "deploy", "plan", "rollback", "compile", "validate", "manifest", "values":
		return "deployments"
	case "logs", "run":
		return "runs"
	case "cluster":
		return "cluster"
	case "dev":
		return "development"
	case "backup":
		return "backups"
	case "bucket":
		return "buckets"
	case "database", "sql", "exec":
		return "databases"
	case "route":
		return "routes"
	case "access", "env", "project":
		return "access"
	case "remote", "upgrade", "version", "completion", "skill", "help":
		return "setup"
	default:
		panic("assign a workflow guide to skali " + first)
	}
}

func cliCommandEntry(out *strings.Builder, command *cobra.Command) {
	fmt.Fprintf(out, "```text\n%s\n```\n", command.UseLine())
	if len(command.Aliases) > 0 {
		fmt.Fprintf(out, "\nAliases: `%s`.\n", strings.Join(command.Aliases, "`, `"))
	}
	description := command.Long
	if description == "" {
		description = command.Short
	}
	fmt.Fprintf(out, "\n%s\n", strings.TrimSpace(description))
	if command.Deprecated != "" {
		fmt.Fprintf(out, "\nDeprecated: %s\n", command.Deprecated)
	}
	if command.Example != "" {
		fmt.Fprintf(out, "\n```sh\n%s\n```\n", strings.TrimSpace(command.Example))
	}
	fmt.Fprintln(out, "\n| Flag | Type | Default | Meaning |")
	fmt.Fprintln(out, "| --- | --- | --- | --- |")
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
		fmt.Fprintf(out, "| %s | `%s` | `%s` | %s |\n", name, f.Value.Type(), cliTableCell(def), cliTableCell(meaning))
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
		fmt.Fprintf(out, "\n%s\n", rule)
	}
}

func cliTableCell(s string) string {
	return strings.NewReplacer("|", "\\|", "\n", "<br>").Replace(s)
}
