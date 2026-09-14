package main

import (
	"strings"

	"github.com/spf13/cobra"
)

// invocation is what the dispatcher needs to know about a command line
// before cobra parses it: which top-level command runs, the one-shot
// --remote and --manifest values that steer remote resolution, and the
// switches that never dispatch.
type invocation struct {
	command  string // top-level command word; "" for bare skali or an unknown command
	path     string // full command path below the root, for example "dev prune"
	remote   string
	manifest string
	help     bool
	version  bool
	verbose  bool
}

// noDispatchCommands always run in the invoked binary: they manage remotes
// and the binary itself, or work without any remote. Config-writing
// commands in particular must stay home: an older writer would drop config
// fields it does not know. The remote subcommands that talk to a daemon
// (add, login, status) hand themselves to its release once their own probe
// has named it (dispatchTo). dev dispatches like a workflow command: the
// local platform runs at the release of the project's target cluster, each
// release in its own cluster (docs/versioning.md, decision 5).
var noDispatchCommands = map[string]bool{
	"version":    true,
	"upgrade":    true,
	"completion": true,
	"help":       true,
	"remote":     true,
	"cluster":    true,
	"skill":      true,
}

// noDispatchPaths pins single subcommands of dispatching groups to home.
// dev prune reasons about every local platform on the machine; home is the
// newest release in use and the only one that knows every record layout.
var noDispatchPaths = map[string]bool{
	"dev prune": true,
}

// preparseArgs reads the command line the way the dispatcher needs it,
// without executing anything. Flags are scanned up to a literal --; the
// command word is resolved with cobra's own Find, which strips flags using
// the real definitions (so `skali --verbose deploy` and `skali run list
// --remote khz` resolve correctly). The help and completion commands cobra
// adds only inside Execute are recognized by name.
func preparseArgs(args []string, root func() *cobra.Command) invocation {
	var inv invocation
	var words []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		switch {
		case arg == "-h" || arg == "--help":
			inv.help = true
		case arg == "--version":
			inv.version = true
		case arg == "--verbose":
			inv.verbose = true
		case arg == "--remote" || arg == "--manifest":
			if i+1 < len(args) {
				i++
				words = append(words, arg, args[i])
				if arg == "--remote" {
					inv.remote = args[i]
				} else {
					inv.manifest = args[i]
				}
				continue
			}
		case strings.HasPrefix(arg, "--remote="):
			inv.remote = strings.TrimPrefix(arg, "--remote=")
		case strings.HasPrefix(arg, "--manifest="):
			inv.manifest = strings.TrimPrefix(arg, "--manifest=")
		}
		words = append(words, arg)
	}
	if len(words) > 0 {
		switch words[0] {
		case "help":
			inv.help = true
			inv.command = "help"
			return inv
		case cobra.ShellCompRequestCmd, cobra.ShellCompNoDescRequestCmd:
			inv.command = "completion"
			return inv
		}
	}
	tree := root()
	found, _, err := tree.Find(words)
	if err != nil || found == nil || found == tree {
		return inv
	}
	inv.path = strings.TrimPrefix(found.CommandPath(), tree.Name()+" ")
	for found.HasParent() && found.Parent().HasParent() {
		found = found.Parent()
	}
	inv.command = found.Name()
	return inv
}
