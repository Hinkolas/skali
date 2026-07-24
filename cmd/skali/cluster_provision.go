package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Hinkolas/skali/internal/cliprompt"
	"github.com/Hinkolas/skali/internal/installer/host"
	"github.com/Hinkolas/skali/internal/installer/limavm"
)

// useManagedLima makes a previously provisioned Lima visible to this
// process: when no limactl is on PATH but the managed prefix holds one,
// its bin directory is prepended to PATH. Every limactl invocation
// (limavm, host.Lima, the LaunchAgent path resolution) resolves through
// the process PATH, so this one mutation covers them all. An existing
// limactl (brew or otherwise) always wins.
func useManagedLima() {
	if runtime.GOOS != "darwin" {
		return
	}
	if _, err := exec.LookPath("limactl"); err == nil {
		return
	}
	prefix, err := limavm.ManagedLimaPrefix()
	if err != nil {
		return
	}
	bin := filepath.Join(prefix, "bin")
	if info, err := os.Stat(filepath.Join(bin, "limactl")); err == nil && info.Mode().IsRegular() {
		os.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
}

// ensureDarwinDeps provisions what the requested network is missing on
// this Mac: Lima itself (rootless, managed prefix), the default
// networks.yaml, socket_vmnet, and the Lima sudoers file. Everything
// missing lands in one printed plan behind one confirmation; --yes skips
// the confirmation and non-interactive runs without it fail naming the
// gaps. Steps run as plain printed lines, never inside the live task
// printer: sudo prompts for its password on the terminal. Each step is
// idempotent, so an interrupted run resumes with only the missing pieces.
func ensureDarwinDeps(ctx context.Context, out *os.File, reader *bufio.Reader, network limavm.Network, assumeYes bool) error {
	if runtime.GOOS != "darwin" || darwinInfo != nil {
		return nil
	}
	mac := host.Local{}
	state, err := limavm.Assess(ctx, mac, network)
	if err != nil {
		return err
	}
	if state.NeedsVMNet && state.NetworksFileExists && !state.NetworkConfigured {
		// The one gap provisioning never closes: an existing networks.yaml
		// is operator configuration and is never edited.
		return limavm.NetworkNotConfiguredError(network, state.NetworksFilePath)
	}
	if !state.Missing() {
		return nil
	}

	type step struct {
		title string
		run   func() error
	}
	var steps []step
	var plan, gaps []string
	if state.LimactlPath == "" {
		prefix, err := limavm.ManagedLimaPrefix()
		if err != nil {
			return err
		}
		gaps = append(gaps, "Lima")
		plan = append(plan, fmt.Sprintf("install Lima v%s into %s (rootless)", limavm.LimaVersion, prefix))
		steps = append(steps, step{"Install Lima v" + limavm.LimaVersion, func() error {
			if _, err := limavm.InstallLima(ctx, mac); err != nil {
				return err
			}
			useManagedLima()
			return nil
		}})
	}
	if state.NeedsVMNet && !state.NetworksFileExists {
		iface := limavm.DetectDefaultInterface(ctx, mac)
		gaps = append(gaps, "networks.yaml")
		plan = append(plan, fmt.Sprintf("write %s (bridged uses interface %s)", state.NetworksFilePath, iface))
		steps = append(steps, step{"Write " + state.NetworksFilePath, func() error {
			return limavm.WriteDefaultNetworks(ctx, mac, iface)
		}})
	}
	if state.NeedsVMNet && !state.SocketVMNetPresent {
		gaps = append(gaps, "socket_vmnet")
		plan = append(plan, fmt.Sprintf("install socket_vmnet v%s root owned:\n     sudo tar Cxzf / <verified download> opt/socket_vmnet",
			limavm.SocketVMNetVersion))
		steps = append(steps, step{"Install socket_vmnet v" + limavm.SocketVMNetVersion, func() error {
			return limavm.InstallSocketVMNet(ctx, mac)
		}})
	}
	if state.NeedsVMNet && !state.SudoersPresent {
		gaps = append(gaps, "sudoers")
		plan = append(plan, "allow Lima to launch it:\n     limactl sudoers | sudo tee /etc/sudoers.d/lima")
		steps = append(steps, step{"Write " + state.SudoersPath, func() error {
			return limavm.WriteSudoers(ctx, mac)
		}})
	}

	fmt.Fprintf(out, "this Mac is missing dependencies for the %q network:\n", network)
	for i, line := range plan {
		fmt.Fprintf(out, "  %d. %s\n", i+1, line)
	}
	fmt.Fprintln(out)
	if !assumeYes {
		if !cliprompt.Interactive() {
			return fmt.Errorf("this Mac is missing dependencies (%s); "+
				"non-interactive provisioning requires --yes", strings.Join(gaps, ", "))
		}
		if reader == nil {
			reader = bufio.NewReader(os.Stdin)
		}
		confirmed, err := promptSession(out, reader).Confirm(ctx, cliprompt.ConfirmOptions{
			Title: "Install these dependencies?",
		})
		if err != nil {
			return err
		}
		if !confirmed {
			return fmt.Errorf("provisioning cancelled; nothing was installed")
		}
	}
	for _, s := range steps {
		if err := s.run(); err != nil {
			return err
		}
		fmt.Fprintf(out, "  ok  %s\n", s.title)
	}
	fmt.Fprintln(out)
	return nil
}
