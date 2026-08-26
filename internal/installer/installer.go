// Package installer is the engine behind the `skali cluster` group, the privileged
// installation and recovery surface of the CLI. It owns host-level k3s lifecycle, the
// root-owned installation record, and the installer-owned skali-system
// bundle converge. It never depends on the Skali API or product database;
// its authority is exactly what skalid must not have. All host mutation
// goes through host.Runner so the same engine drives a local Linux host, a
// scripted test fake, and a Lima-managed VM.
package installer

import "github.com/Hinkolas/skali/internal/bundle"

// K3sVersion is the k3s release this installer provisions. Each installer
// release pins exactly one; it must agree with the k3d image pin in
// internal/localdev (guarded by a test). A var only so e2e test builds can
// rebase the pin via -ldflags -X; release builds never set it.
var K3sVersion = "v1.36.3+k3s1"

const (
	// StateDir is the root-owned installation state directory.
	StateDir = "/var/lib/skali"
	// RecordPath is the root-owned installation record.
	RecordPath = StateDir + "/installation.yaml"
	// RecordBackupPath retains the last valid record so a truncated or
	// interrupted write never removes the installer's recovery authority.
	RecordBackupPath = StateDir + "/installation.yaml.prev"
	// LogDir receives per-run installer logs. Installer steps run before
	// or below the product control plane, so they log locally, never to
	// the product run journal.
	LogDir = StateDir + "/logs"
	// CacheDir holds fetched or unpacked installer inputs.
	CacheDir = StateDir + "/cache"

	// ProviderK3s marks installations whose Kubernetes lifecycle this
	// installer owns.
	ProviderK3s = "k3s"

	// OwnershipManaged means the installer owns host-level k3s lifecycle.
	OwnershipManaged = "managed"

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
func (silentProgress) Note(string)  {}
