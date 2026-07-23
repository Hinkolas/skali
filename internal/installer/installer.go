// Package installer is the engine behind the `skali cluster` group, the privileged
// installation and recovery surface of the CLI. It owns host-level k3s lifecycle, the
// root-owned installation record, and the installer-owned skali-system
// bundle converge. It never depends on the Skali API or product database
// (section 14.1); its authority is exactly what skalid must not have
// (section 14.5). All host mutation goes through host.Runner so the same
// engine drives a local Linux host, a scripted test fake, and later a
// Lima-managed VM.
package installer

import "github.com/Hinkolas/skali/internal/bundle"

// K3sVersion is the k3s release this installer provisions. Each installer
// release pins exactly one; it must agree with the k3d image pin in
// internal/localdev (guarded by a test). A var only so e2e test builds can
// rebase the pin via -ldflags -X; release builds never set it.
var K3sVersion = "v1.33.3+k3s1"

const (
	// StateDir is the root-owned installation state directory.
	StateDir = "/var/lib/skali"
	// RecordPath is the root-owned installation record (section 14.1).
	RecordPath = StateDir + "/installation.yaml"
	// LogDir receives per-run installer logs. Installer steps run before
	// or below the product control plane, so they log locally, never to
	// the product run journal.
	LogDir = StateDir + "/logs"
	// CacheDir holds fetched or unpacked installer inputs.
	CacheDir = StateDir + "/cache"

	// ProviderK3s marks installations whose Kubernetes lifecycle this
	// installer owns. ProviderExternal marks existing-cluster
	// installations, whose hosts skali does not administer.
	ProviderK3s      = "k3s"
	ProviderExternal = "external"

	// OwnershipManaged means the installer owns host-level k3s lifecycle.
	// OwnershipExistingCluster means the installer owns only the Skali
	// system bundle in a cluster it does not administer.
	OwnershipManaged         = "managed"
	OwnershipExistingCluster = "existing-cluster"

	// DefaultCluster names the cluster when the operator does not.
	DefaultCluster = "production"
)

// Progress narrates engine stages: Start begins a stage, Done or Skip
// concludes it. A stage that errors is never concluded; the caller settles
// it from the returned error. The shape is shared with the bundle converge.
type Progress = bundle.Progress

type silentProgress struct{}

func (silentProgress) Start(string) {}
func (silentProgress) Done(string)  {}
func (silentProgress) Skip(string)  {}

// progressNoter is the optional Progress capability to publish a transient
// status line under the running stage (the spinner tail). The interactive
// task printer implements it; the silent and plain progresses do not, and
// note is a no-op for them.
type progressNoter interface {
	Note(line string)
}

// note publishes a transient status line when the progress supports it,
// used to surface what a long-running stage is waiting on.
func note(progress Progress, line string) {
	if noter, ok := progress.(progressNoter); ok {
		noter.Note(line)
	}
}
