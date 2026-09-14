package main

import (
	"strings"

	"github.com/spf13/cobra"

	versionpkg "github.com/Hinkolas/skali/internal/version"
)

// invocation is what the dispatcher needs to know about a command line
// before cobra parses it: which top-level command runs, the one-shot
// --remote and --manifest values that steer remote resolution, and the
// switches that never dispatch.
type invocation struct {
	command    string // top-level command word; "" for bare skali or an unknown command
	path       string // full command path below the root, for example "dev start"
	remote     string
	manifest   string
	help       bool
	version    bool
	offline    bool
	completion bool
	recover    bool
	imageTar   bool
	verbose    bool
}

// Home policies cover binary installation and remote configuration. API work
// in remote commands performs an explicit pre-execution handoff after resolving
// its positional target. Host cluster controls use the policy below.
var noDispatchCommands = map[string]bool{
	"version":    true,
	"upgrade":    true,
	"completion": true,
	"help":       true,
}

// Local lifecycle and skill installation never depend on a remote.
var noDispatchPaths = map[string]bool{
	"dev stop":      true,
	"dev reset":     true,
	"dev status":    true,
	"skill install": true,
}

// Older prereleases did not implement the worker/context contract.
func unsupportedDispatchRelease(release string) bool {
	return versionpkg.Older(release, minimumDispatchRelease)
}

// homeInvocation distinguishes host control from API workflows. A command's
// help follows the same policy as its execution.
func homeInvocation(inv invocation) bool {
	if inv.command == "remote" {
		switch inv.path {
		case "remote", "remote list", "remote use", "remote token":
			return true
		case "remote add", "remote login", "remote logout", "remote remove", "remote revoke-session":
			// Their positional targets are resolved by the command before API work.
			return !inv.help || inv.path == "remote add"
		}
	}
	if inv.command == "cluster" {
		return inv.path != "cluster upgrade" || inv.recover || inv.imageTar
	}
	return noDispatchCommands[inv.command] || noDispatchPaths[inv.path]
}

func readOnlyInvocation(inv invocation) bool {
	if inv.help || inv.completion {
		return true
	}
	switch inv.path {
	case "validate", "compile", "skill read", "logs", "env list", "run list", "run show", "run logs", "backup list", "backup target show", "access list", "remote status":
		return true
	}
	return false
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
		case arg == "--offline" || arg == "--offline=true":
			inv.offline = true
		case arg == "--recover" || arg == "--recover=true":
			inv.recover = true
		case arg == "--image-tar" || strings.HasPrefix(arg, "--image-tar="):
			inv.imageTar = true
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
			if len(words) == 1 {
				inv.command = "help"
				return inv
			}
			words = words[1:]
		case cobra.ShellCompRequestCmd, cobra.ShellCompNoDescRequestCmd:
			inv.completion = true
			words = words[1:]
		}
	}
	tree := root()
	found, remaining, err := tree.Find(words)
	if err != nil || found == nil || found == tree {
		// The target may introduce a command unknown to this launcher. Preserve
		// the original arguments and let the selected release interpret it.
		if len(words) > 0 && !strings.HasPrefix(words[0], "-") {
			inv.command = words[0]
			inv.path = words[0]
		}
		return inv
	}
	inv.path = strings.TrimPrefix(found.CommandPath(), tree.Name()+" ")
	if (inv.path == "remote login" || inv.path == "remote logout" || inv.path == "remote remove") && len(remaining) > 0 && !strings.HasPrefix(remaining[0], "-") {
		inv.remote = remaining[0]
	}
	for found.HasParent() && found.Parent().HasParent() {
		found = found.Parent()
	}
	inv.command = found.Name()
	if inv.command != "" {
		inv.version = false
	}
	return inv
}
