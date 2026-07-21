package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// stubCommands reserves the transcript's command surface for operations of
// later slices; each fails with a named refusal instead of not existing.
func stubCommands() []*cobra.Command {
	stubs := []struct {
		use, short, what string
	}{
		{"token", "Print the join command for this cluster", "multi-node enrollment"},
		{"join", "Join this host to an existing cluster", "multi-node enrollment"},
		{"diagnose", "Diagnose the installation from host state alone", "diagnosis"},
		{"repair", "Repair a damaged installation", "repair"},
		{"upgrade", "Upgrade k3s or the Skali bundle", "upgrades"},
	}
	commands := make([]*cobra.Command, 0, len(stubs))
	for _, stub := range stubs {
		commands = append(commands, &cobra.Command{
			Use:   stub.use,
			Short: stub.short,
			RunE: func(what string) func(*cobra.Command, []string) error {
				return func(*cobra.Command, []string) error {
					return fmt.Errorf("not implemented in this slice: %s arrives with a later milestone", what)
				}
			}(stub.what),
		})
	}
	return commands
}
