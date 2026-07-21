package limavm

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/Hinkolas/skali/internal/installer/host"
)

// Defaults Lima applies when networks.yaml omits the paths section; they
// match the file Lima generates.
const (
	defaultSocketVMNetPath = "/opt/socket_vmnet/bin/socket_vmnet"
	defaultSudoersPath     = "/private/etc/sudoers.d/lima"
)

// socketVMNetSetup is the one time root owned setup the installer refuses
// to perform itself; it is printed verbatim whenever a vmnet network is
// requested but not ready. The pinned version was current at
// implementation time; any release works.
const socketVMNetSetup = `complete the one time socket_vmnet setup, then run skali-installer again:
  1. install socket_vmnet root owned (see https://lima-vm.io/docs/config/network/):
       curl -OSL https://github.com/lima-vm/socket_vmnet/releases/download/v1.2.2/socket_vmnet-1.2.2-$(uname -m).tar.gz
       sudo tar Cxzvf / socket_vmnet-1.2.2-$(uname -m).tar.gz opt/socket_vmnet
  2. allow Lima to launch it:
       limactl sudoers | sudo tee /etc/sudoers.d/lima`

// limaNetworks is the slice of ~/.lima/_config/networks.yaml this package
// consumes.
type limaNetworks struct {
	Paths struct {
		SocketVMNet string `yaml:"socketVMNet"`
		Sudoers     string `yaml:"sudoers"`
	} `yaml:"paths"`
	Networks map[string]struct {
		Mode string `yaml:"mode"`
	} `yaml:"networks"`
}

// Preflight verifies that the requested network can carry a VM on this
// Mac. It only checks and instructs; installing Lima or socket_vmnet is
// the operator's one time root owned step.
func Preflight(ctx context.Context, mac host.Runner, network Network) error {
	if _, err := mac.Run(ctx, host.Command{Name: "limactl", Args: []string{"--version"}}); err != nil {
		return errors.New("limactl was not found on PATH; install Lima first: brew install lima")
	}
	if network == NetworkUserV2 {
		// user-v2 ships with Lima and needs no root components.
		return nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home directory: %w", err)
	}
	configPath := filepath.Join(home, ".lima", "_config", "networks.yaml")
	data, err := mac.ReadFile(ctx, configPath)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("the Lima network %q is not configured; %s does not exist\n%s",
			network, configPath, socketVMNetSetup)
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", configPath, err)
	}
	var config limaNetworks
	if err := yaml.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("parse %s: %w", configPath, err)
	}
	if _, ok := config.Networks[string(network)]; !ok {
		return fmt.Errorf("the Lima network %q is not configured in %s\n%s",
			network, configPath, socketVMNetSetup)
	}

	socketVMNet := config.Paths.SocketVMNet
	if socketVMNet == "" {
		socketVMNet = defaultSocketVMNetPath
	}
	info, err := mac.Stat(ctx, socketVMNet)
	if err != nil {
		return fmt.Errorf("stat %s: %w", socketVMNet, err)
	}
	if !info.Exists {
		return fmt.Errorf("socket_vmnet is not installed at %s (configured in %s)\n%s",
			socketVMNet, configPath, socketVMNetSetup)
	}

	sudoers := config.Paths.Sudoers
	if sudoers == "" {
		sudoers = defaultSudoersPath
	}
	info, err = mac.Stat(ctx, sudoers)
	if err != nil {
		return fmt.Errorf("stat %s: %w", sudoers, err)
	}
	if !info.Exists {
		return fmt.Errorf("the Lima sudoers file %s is missing; create it with: limactl sudoers | sudo tee /etc/sudoers.d/lima", sudoers)
	}
	return nil
}
