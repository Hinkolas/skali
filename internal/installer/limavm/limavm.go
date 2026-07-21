// Package limavm manages the one Lima VM that hosts the skali node on a
// Mac: template rendering, create, start, stop, delete, network preflight,
// and the login LaunchAgent. Everything here executes on the Mac itself
// through a host.Runner (host.Local in production, host.Fake in tests);
// all in-guest work goes through host.Lima instead.
package limavm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/Hinkolas/skali/internal/installer/host"
)

const (
	// DefaultInstance is the production VM name; the e2e suites use
	// different names so a developer Mac can host both.
	DefaultInstance = "skali"
	// KubeForwardPort maps guest 6443 to host loopback on the user-v2
	// network, where the Mac cannot reach guest addresses directly.
	KubeForwardPort = 16443
	// LaunchAgentLabel identifies the login item that starts the VM.
	LaunchAgentLabel = "dev.skali.lima"
)

// Network is the Lima network the VM attaches to. Bridged and shared run
// over socket_vmnet (root owned, preflighted); user-v2 needs no setup but
// is reachable only from this Mac and other Lima VMs.
type Network string

const (
	NetworkBridged Network = "bridged"
	NetworkShared  Network = "shared"
	NetworkUserV2  Network = "user-v2"
)

// Spec shapes the VM to create. The Lima instance itself is the single
// source of truth afterwards; there is no separate state file on the Mac.
type Spec struct {
	Instance string
	Network  Network
	CPUs     int
	Memory   string // for example "12GiB"
	Disk     string // for example "100GiB"
	// Hostname becomes the guest hostname and therefore the k3s node name.
	// Lima would otherwise name every guest lima-<instance>, which would
	// collide across a fleet of Macs using the same instance name.
	Hostname string
}

// Info reports what Inspect learned about an instance.
type Info struct {
	Exists  bool
	Running bool
	Network Network
}

// limaListEntry is the slice of `limactl list --format json` output this
// package consumes; unknown fields are ignored.
type limaListEntry struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Network []struct {
		Lima string `json:"lima"`
	} `json:"network"`
	Config struct {
		Networks []struct {
			Lima string `json:"lima"`
		} `json:"networks"`
	} `json:"config"`
}

// Inspect reports whether the named instance exists and runs. An absent
// instance is not an error.
func Inspect(ctx context.Context, mac host.Runner, instance string) (Info, error) {
	result, err := mac.Run(ctx, host.Command{
		Name: "limactl",
		Args: []string{"list", "--format", "json", instance},
	})
	if err != nil {
		return Info{}, err
	}
	// limactl exits nonzero and prints a warning when no instance matches;
	// treat any of that as absent and only trust decodable output.
	output := strings.TrimSpace(result.Stdout)
	if result.ExitCode != 0 || output == "" {
		return Info{}, nil
	}
	var entry limaListEntry
	if err := json.Unmarshal([]byte(output), &entry); err != nil {
		return Info{}, fmt.Errorf("parse limactl list output for VM %s: %w", instance, err)
	}
	if entry.Name != instance {
		return Info{}, nil
	}
	info := Info{Exists: true, Running: entry.Status == "Running"}
	if len(entry.Network) > 0 && entry.Network[0].Lima != "" {
		info.Network = Network(entry.Network[0].Lima)
	} else if len(entry.Config.Networks) > 0 {
		info.Network = Network(entry.Config.Networks[0].Lima)
	}
	return info, nil
}

// DefaultSpec sizes the VM for a dedicated fleet Mac: most of the machine
// goes to the VM, with headroom for macOS itself. Probes that fail fall
// back to the minimums.
func DefaultSpec(ctx context.Context, mac host.Runner, macHostname string) Spec {
	cpus := 2
	if n := probeInt(ctx, mac, "hw.ncpu"); n-1 > cpus {
		cpus = n - 1
	}
	memoryGiB := 4
	if bytes := probeInt(ctx, mac, "hw.memsize"); bytes > 0 {
		if gib := bytes/(1<<30) - 4; gib > memoryGiB {
			memoryGiB = gib
		}
	}
	return Spec{
		Instance: DefaultInstance,
		Network:  NetworkBridged,
		CPUs:     cpus,
		Memory:   fmt.Sprintf("%dGiB", memoryGiB),
		Disk:     "100GiB",
		Hostname: GuestHostname(macHostname),
	}
}

func probeInt(ctx context.Context, mac host.Runner, key string) int {
	result, err := mac.Run(ctx, host.Command{Name: "sysctl", Args: []string{"-n", key}})
	if err != nil || result.ExitCode != 0 {
		return 0
	}
	value, err := strconv.Atoi(strings.TrimSpace(result.Stdout))
	if err != nil {
		return 0
	}
	return value
}

// GuestHostname derives a valid Linux hostname from the Mac's hostname:
// lowercase, a trailing .local stripped, anything outside [a-z0-9-]
// replaced with a dash, repeats collapsed, at most 63 characters.
func GuestHostname(macHostname string) string {
	name := strings.ToLower(strings.TrimSpace(macHostname))
	name = strings.TrimSuffix(name, ".local")
	var builder strings.Builder
	previousDash := false
	for _, r := range name {
		valid := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		switch {
		case valid:
			builder.WriteRune(r)
			previousDash = false
		case !previousDash:
			builder.WriteByte('-')
			previousDash = true
		}
	}
	name = strings.Trim(builder.String(), "-")
	if len(name) > 63 {
		name = strings.Trim(name[:63], "-")
	}
	if name == "" {
		return "skali-node"
	}
	return name
}

// Create renders the VM template and creates plus starts the instance.
// The rendered template lands in a Mac-side temp file because limactl
// reads it from disk.
func Create(ctx context.Context, mac host.Runner, spec Spec) error {
	rendered, err := renderTemplate(spec)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp("", "skali-vm-*.yaml")
	if err != nil {
		return fmt.Errorf("stage VM template: %w", err)
	}
	path := file.Name()
	defer os.Remove(path)
	if _, err := file.Write(rendered); err != nil {
		file.Close()
		return fmt.Errorf("stage VM template: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("stage VM template: %w", err)
	}
	result, err := mac.Run(ctx, host.Command{
		Name: "limactl",
		Args: []string{"start", "--name", spec.Instance, "--tty=false", path},
	})
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("create VM %s: exit %d: %s",
			spec.Instance, result.ExitCode, tail(result.Stderr))
	}
	return nil
}

// Start boots an existing instance. Callers must have confirmed existence
// first: a bare `limactl start <name>` for an unknown name would silently
// create a default VM instead.
func Start(ctx context.Context, mac host.Runner, instance string) error {
	result, err := mac.Run(ctx, host.Command{
		Name: "limactl",
		Args: []string{"start", instance},
	})
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("start VM %s: exit %d: %s", instance, result.ExitCode, tail(result.Stderr))
	}
	return nil
}

// Stop shuts the instance down; an already stopped instance is not an
// error.
func Stop(ctx context.Context, mac host.Runner, instance string) error {
	result, err := mac.Run(ctx, host.Command{
		Name: "limactl",
		Args: []string{"stop", instance},
	})
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		info, inspectErr := Inspect(ctx, mac, instance)
		if inspectErr == nil && (!info.Exists || !info.Running) {
			return nil
		}
		return fmt.Errorf("stop VM %s: exit %d: %s", instance, result.ExitCode, tail(result.Stderr))
	}
	return nil
}

// Delete removes the instance and its disk; an absent instance is not an
// error.
func Delete(ctx context.Context, mac host.Runner, instance string) error {
	result, err := mac.Run(ctx, host.Command{
		Name: "limactl",
		Args: []string{"delete", "--force", instance},
	})
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		info, inspectErr := Inspect(ctx, mac, instance)
		if inspectErr == nil && !info.Exists {
			return nil
		}
		return fmt.Errorf("delete VM %s: exit %d: %s", instance, result.ExitCode, tail(result.Stderr))
	}
	return nil
}

// tail trims stderr to its last non-empty line, which is where limactl
// puts its fatal message.
func tail(stderr string) string {
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return "no output"
}
