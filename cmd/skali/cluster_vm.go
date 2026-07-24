package main

// On macOS `skali cluster` manages one headless Linux VM via Lima and
// installs the node inside it: Kubernetes nodes are Linux-only, so the VM
// is the node and the Mac is its chassis. Every function here is a no-op
// on Linux, where the engine keeps running against the host directly.
// Privileged work happens inside the VM through host.Lima; the only sudo
// on the Mac itself is the confirmed dependency-provisioning step
// (cluster_provision.go).

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"github.com/Hinkolas/skali/internal/cliprompt"
	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/installer"
	"github.com/Hinkolas/skali/internal/installer/host"
	"github.com/Hinkolas/skali/internal/installer/limavm"
	"github.com/Hinkolas/skali/internal/layout"
)

// vmPolicy states what a command needs from the managed VM.
type vmPolicy int

const (
	// vmPolicyInstall tolerates an absent VM; the install path creates it.
	vmPolicyInstall vmPolicy = iota
	// vmPolicyStatus reports an absent VM as a fresh Mac.
	vmPolicyStatus
	// vmPolicyMaintain requires the VM: maintenance needs a target.
	vmPolicyMaintain
)

// darwinVM describes the resolved managed VM for header decoration.
type darwinVM struct {
	Instance string
	Network  limavm.Network
}

var (
	// resolvedVM is the instance name the prelude settled on, set on
	// darwin even when no VM exists yet.
	resolvedVM string
	// darwinInfo is non-nil once the engine runner points into a running
	// VM; Linux runs leave it nil.
	darwinInfo     *darwinVM
	preludeDone    bool
	preludePresent bool
)

// darwinPrelude resolves the managed VM before any engine call: on a Mac
// the record and all installation state live inside the VM, so nothing can
// be detected without it. It returns present=false when no VM exists; when
// one exists it is started if stopped and the engine runner becomes
// host.Lima. On Linux it only rejects a changed --vm flag.
func darwinPrelude(ctx context.Context, out *os.File, policy vmPolicy, configVMName string) (bool, error) {
	if runtime.GOOS != "darwin" {
		if vmFlagChanged() {
			return false, errors.New("the --vm flag applies only to macOS hosts")
		}
		return true, nil
	}
	if preludeDone {
		if !preludePresent && policy == vmPolicyMaintain {
			return false, vmAbsentError()
		}
		return preludePresent, nil
	}
	name, err := resolveVMName(vmFlagChanged(), vmFlag, configVMName)
	if err != nil {
		return false, err
	}
	resolvedVM = name
	if _, err := exec.LookPath("limactl"); err != nil {
		// No Lima on this Mac at all: that is a fresh Mac, not an error.
		// The install flow provisions Lima before creating the VM, and
		// maintenance has nothing to target.
		preludeDone, preludePresent = true, false
		if policy == vmPolicyMaintain {
			return false, vmAbsentError()
		}
		return false, nil
	}
	mac := host.Local{}
	info, err := limavm.Inspect(ctx, mac, name)
	if err != nil {
		return false, err
	}
	if !info.Exists {
		preludeDone, preludePresent = true, false
		if policy == vmPolicyMaintain {
			return false, vmAbsentError()
		}
		return false, nil
	}
	if !info.Running {
		// The LaunchAgent normally keeps the VM running; a stopped VM is
		// the anomaly, and starting it mutates no installation state.
		fmt.Fprintf(out, "starting VM %s (stopped)\n\n", name)
		if err := limavm.Start(ctx, mac, name); err != nil {
			return false, err
		}
	}
	activeRunner = host.Lima{Instance: name}
	darwinInfo = &darwinVM{Instance: name, Network: info.Network}
	preludeDone, preludePresent = true, true
	return true, nil
}

func vmAbsentError() error {
	return fmt.Errorf("no skali VM named %q exists on this Mac; run skali cluster install first", resolvedVM)
}

// resolveVMName picks the Lima instance name: an explicit config vm.name
// wins over the flag default, and a changed flag must agree with it.
func resolveVMName(flagChanged bool, flagValue, configName string) (string, error) {
	if flagChanged && configName != "" && configName != flagValue {
		return "", fmt.Errorf("the --vm flag (%q) and the config vm.name (%q) disagree", flagValue, configName)
	}
	if configName != "" {
		return configName, nil
	}
	return flagValue, nil
}

// ensureDarwinVM creates the managed VM when it does not exist yet and
// points the engine runner into it. The vm config may be nil; every unset
// field takes the fleet default.
func ensureDarwinVM(ctx context.Context, progress *taskProgress, vm *installer.VMConfig) error {
	if runtime.GOOS != "darwin" || darwinInfo != nil {
		return nil
	}
	mac := host.Local{}
	hostname, _ := os.Hostname()
	spec := limavm.DefaultSpec(ctx, mac, hostname)
	spec.Instance = resolvedVM
	if vm != nil {
		if vm.Network != "" {
			spec.Network = limavm.Network(vm.Network)
		}
		if vm.CPUs > 0 {
			spec.CPUs = vm.CPUs
		}
		if vm.Memory != "" {
			spec.Memory = vm.Memory
		}
		if vm.Disk != "" {
			spec.Disk = vm.Disk
		}
	}
	if err := limavm.Preflight(ctx, mac, spec.Network); err != nil {
		return err
	}
	progress.Start(fmt.Sprintf("Create VM %s (%s, %d cpus, %s memory, %s disk)",
		spec.Instance, spec.Network, spec.CPUs, spec.Memory, spec.Disk))
	if err := limavm.Create(ctx, mac, spec); err != nil {
		return err
	}
	activeRunner = host.Lima{Instance: spec.Instance}
	darwinInfo = &darwinVM{Instance: spec.Instance, Network: spec.Network}
	preludeDone, preludePresent = true, true
	return nil
}

// runDarwinVMPrompts collects the VM shape interactively before the fresh
// flow proper, and creates the VM. No-op when one already runs.
func runDarwinVMPrompts(ctx context.Context, out *os.File, reader *bufio.Reader) error {
	if runtime.GOOS != "darwin" || darwinInfo != nil {
		return nil
	}
	mac := host.Local{}
	hostname, _ := os.Hostname()
	defaults := limavm.DefaultSpec(ctx, mac, hostname)

	network, err := promptSession(out, reader).Select(ctx, cliprompt.SelectOptions{
		Title:       "How should the VM connect to the network?",
		Description: "Bridged provides a LAN address; shared and user-v2 use NAT.",
		Options: []cliprompt.Option{
			{Label: "bridged", Description: "LAN-visible address", Value: string(limavm.NetworkBridged)},
			{Label: "shared", Description: "socket_vmnet NAT", Value: string(limavm.NetworkShared)},
			{Label: "user-v2", Description: "rootless user-mode NAT", Value: string(limavm.NetworkUserV2)},
		},
		DefaultValue: string(defaults.Network),
	})
	if err != nil {
		return err
	}
	cpusAnswer, err := cliprompt.LineDefault(reader,
		"  vm cpus ["+strconv.Itoa(defaults.CPUs)+"]: ", strconv.Itoa(defaults.CPUs))
	if err != nil {
		return err
	}
	cpus, err := strconv.Atoi(strings.TrimSpace(cpusAnswer))
	if err != nil || cpus < 1 {
		return fmt.Errorf("vm cpus must be a positive count, got %q", cpusAnswer)
	}
	memory, err := cliprompt.LineDefault(reader, "  vm memory ["+defaults.Memory+"]: ", defaults.Memory)
	if err != nil {
		return err
	}
	disk, err := cliprompt.LineDefault(reader, "  vm disk ["+defaults.Disk+"]: ", defaults.Disk)
	if err != nil {
		return err
	}
	vm := &installer.VMConfig{Network: network, CPUs: cpus, Memory: memory, Disk: disk}
	if err := vm.Validate(); err != nil {
		return err
	}
	fmt.Fprintln(out)

	// Dependency provisioning runs before the task printer starts: sudo
	// may prompt for a password.
	if err := ensureDarwinDeps(ctx, out, reader, limavm.Network(vm.Network), false); err != nil {
		return err
	}

	tasks := clirender.NewTasks(out)
	progress := newTaskProgress(tasks)
	if err := ensureDarwinVM(ctx, progress, vm); err != nil {
		progress.Abort()
		return err
	}
	progress.Done("")
	fmt.Fprintln(out)
	return nil
}

// applyDarwinInstallOptions defaults the advertised node address to the
// VM's own and resolves a join token file on the Mac side: the engine
// would otherwise read the path inside the VM, where it does not exist.
// No-op on Linux, where the runner is the same machine.
func applyDarwinInstallOptions(ctx context.Context, opts *installer.InstallOptions) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	if opts.NodeIP == "" {
		ip, err := darwinNodeIP(ctx)
		if err != nil {
			return err
		}
		opts.NodeIP = ip
	}
	if opts.Join != nil && opts.Join.TokenFile != "" {
		data, err := os.ReadFile(opts.Join.TokenFile)
		if err != nil {
			return fmt.Errorf("read join token file %s: %v", opts.Join.TokenFile, err)
		}
		token := strings.TrimSpace(string(data))
		if token == "" {
			return fmt.Errorf("join token file %s is empty", opts.Join.TokenFile)
		}
		opts.Join.Token = token
		opts.Join.TokenFile = ""
	}
	return nil
}

// darwinNodeIP reads the VM's address on the interface other nodes reach
// it through: eth0 on user-v2 (which replaces the default NIC), lima0 on
// the vmnet networks. Pinning it makes the advertised address stable and
// puts it in the k3s certificate SANs.
func darwinNodeIP(ctx context.Context) (string, error) {
	device := "lima0"
	if darwinInfo == nil {
		return "", errors.New("no VM runner is active")
	}
	if darwinInfo.Network == limavm.NetworkUserV2 || darwinInfo.Network == "" {
		device = "eth0"
	}
	result, err := runner().Run(ctx, host.Command{
		Name: "ip", Args: []string{"-4", "-o", "addr", "show", "dev", device},
	})
	if err != nil {
		return "", err
	}
	for line := range strings.SplitSeq(result.Stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[2] != "inet" {
			continue
		}
		if address, _, ok := strings.Cut(fields[3], "/"); ok {
			return address, nil
		}
	}
	return "", fmt.Errorf("detect the VM address on %s: no IPv4 address; the VM network may still be acquiring a DHCP lease", device)
}

// finishDarwinInstall registers the login LaunchAgent after a successful
// install. The installation itself already succeeded, so every problem
// here degrades to a returned warning line rather than an error.
func finishDarwinInstall(ctx context.Context, progress *taskProgress) []string {
	if runtime.GOOS != "darwin" || darwinInfo == nil {
		return nil
	}
	mac := host.Local{}
	var warnings []string
	progress.Start("Install login LaunchAgent")
	_, err := limavm.InstallLaunchAgent(ctx, mac, darwinInfo.Instance)
	switch {
	case err == nil:
		progress.Done("")
	case errors.Is(err, limavm.ErrLaunchAgentLoad):
		progress.Done("loads at next login")
		warnings = append(warnings, "warning: could not load the launch agent into this session; it loads at the next login")
	default:
		progress.Skip("failed")
		warnings = append(warnings, "warning: "+err.Error())
	}
	if limavm.AutoLoginUser(ctx, mac) == "" {
		warnings = append(warnings, "warning: macOS auto login is disabled; after a reboot the skali VM stays "+
			"down until someone logs in. Enable auto login in System Settings under Users and Groups for "+
			"headless operation.")
	}
	return warnings
}

func printWarnings(out *os.File, warnings []string) {
	if len(warnings) == 0 {
		return
	}
	fmt.Fprintln(out)
	for _, warning := range warnings {
		fmt.Fprintln(out, warning)
	}
}

// printDarwinFreshHeader renders the state block for a Mac without a VM;
// the regular header would need a runner target that does not exist yet.
func printDarwinFreshHeader(out *os.File) {
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		hostname = "(unknown)"
	}
	fmt.Fprintf(out, "host %s: fresh\n", hostname)
	fmt.Fprintf(out, "  os      macOS (darwin/%s)\n", runtime.GOARCH)
	fmt.Fprintln(out, "  vm      none (installing creates a Linux VM via Lima)")
	fmt.Fprintln(out, "  k3s     not installed")
	fmt.Fprintln(out, "  record  none")
	fmt.Fprintln(out)
}

// readHostFile reads a path the operator named next to the config file: on
// the Mac in darwin mode, through the runner on Linux (where the runner is
// this machine anyway).
func readHostFile(ctx context.Context, path string) ([]byte, error) {
	if runtime.GOOS == "darwin" {
		return os.ReadFile(path)
	}
	return runner().ReadFile(ctx, path)
}

// uninstallDarwinNode removes the node by deleting the VM: that removes
// k3s, the record, and all state at once, so the in-guest uninstall would
// only slow the teardown.
func uninstallDarwinNode(ctx context.Context, out *os.File, record *installer.Record) error {
	mac := host.Local{}
	tasks := clirender.NewTasks(out)
	progress := newTaskProgress(tasks)
	progress.Start("Stop VM " + darwinInfo.Instance)
	if err := limavm.Stop(ctx, mac, darwinInfo.Instance); err != nil {
		progress.Abort()
		return err
	}
	progress.Start("Delete VM " + darwinInfo.Instance)
	if err := limavm.Delete(ctx, mac, darwinInfo.Instance); err != nil {
		progress.Abort()
		return err
	}
	progress.Start("Remove login LaunchAgent")
	if err := limavm.RemoveLaunchAgent(ctx, mac); err != nil {
		progress.Abort()
		return err
	}
	progress.Done("")
	fmt.Fprintln(out, "\nThis Mac is fresh again.")
	if record.Node.Role == layout.RoleAgent {
		fmt.Fprintf(out, "The node object %s remains in the cluster; "+
			"delete it from a server with `k3s kubectl delete node %s`.\n",
			record.Node.Name, record.Node.Name)
	}
	return nil
}
