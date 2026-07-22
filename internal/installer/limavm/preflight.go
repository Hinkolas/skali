package limavm

import (
	"context"
	"errors"
	"fmt"

	"github.com/Hinkolas/skali/internal/installer/host"
)

// Defaults Lima applies when networks.yaml omits the paths section; they
// match the file Lima generates.
const (
	defaultSocketVMNetPath = "/opt/socket_vmnet/bin/socket_vmnet"
	defaultSudoersPath     = "/private/etc/sudoers.d/lima"
)

// Preflight verifies that the requested network can carry a VM on this
// Mac. It is the last read-only guard before VM creation: provisioning
// (provision.go, driven by the command layer) is expected to have closed
// every gap it names.
func Preflight(ctx context.Context, mac host.Runner, network Network) error {
	state, err := Assess(ctx, mac, network)
	if err != nil {
		return err
	}
	switch {
	case state.LimactlPath == "":
		return errors.New("limactl was not found on PATH; skali cluster install provisions Lima, " +
			"or install it yourself: brew install lima")
	case !state.NeedsVMNet:
		// user-v2 ships with Lima and needs no root components.
		return nil
	case !state.NetworksFileExists:
		return fmt.Errorf("the Lima network %q is not configured; %s does not exist; "+
			"skali cluster install provisions it", network, state.NetworksFilePath)
	case !state.NetworkConfigured:
		return NetworkNotConfiguredError(network, state.NetworksFilePath)
	case !state.SocketVMNetPresent:
		return fmt.Errorf("socket_vmnet is not installed at %s (configured in %s); "+
			"skali cluster install provisions it", state.SocketVMNetPath, state.NetworksFilePath)
	case !state.SudoersPresent:
		return fmt.Errorf("the Lima sudoers file %s is missing; skali cluster install provisions it, "+
			"or create it yourself: limactl sudoers | sudo tee /etc/sudoers.d/lima", state.SudoersPath)
	}
	return nil
}
