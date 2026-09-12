package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// TestCommandVerbConsistency keeps the noun-verb surface uniform: the
// canonical spellings are list and remove, every one of them also answers
// to ls and rm, no command is named by the short form, and argument
// placeholders use one notation. Refs #18.
func TestCommandVerbConsistency(t *testing.T) {
	t.Parallel()
	var walk func(command *cobra.Command)
	walk = func(command *cobra.Command) {
		path := command.CommandPath()
		switch command.Name() {
		case "list":
			require.Contains(t, command.Aliases, "ls", "%s lacks the ls alias", path)
		case "remove":
			require.Contains(t, command.Aliases, "rm", "%s lacks the rm alias", path)
		case "ls", "rm", "delete", "del":
			t.Errorf("%s: use list or remove as the canonical verb", path)
		}
		for _, token := range strings.Fields(command.Use)[1:] {
			if strings.HasPrefix(token, "-") {
				continue
			}
			bare := strings.TrimSuffix(token, "...")
			if strings.HasPrefix(bare, "<") && strings.HasSuffix(bare, ">") {
				continue
			}
			if bare == "--" {
				continue
			}
			if token == "[flags]" || (strings.HasPrefix(bare, "[") && strings.HasSuffix(bare, "]")) {
				continue
			}
			t.Errorf("%s: placeholder %q must be <name>, [name], or --", path, token)
		}
		for _, child := range command.Commands() {
			walk(child)
		}
	}
	walk(newRootCommand())
}
