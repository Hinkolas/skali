package installer

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/Hinkolas/skali/internal/bundle"
	"github.com/Hinkolas/skali/internal/installer/host"
	"github.com/Hinkolas/skali/internal/layout"
	"github.com/Hinkolas/skali/internal/version"
)

// UpgradePlan states what an upgrade would change on this host, computed
// purely from a gathered status so planning stays read-only.
type UpgradePlan struct {
	K3sFrom string
	K3sTo   string
	// K3sDrifted means the installed k3s does not match this installer's
	// pin; K3sDowngrade that it is newer than the pin, which an upgrade
	// must refuse (k3s does not support downgrades against an existing
	// datastore).
	K3sDrifted   bool
	K3sDowngrade bool
	// K3sMinorSkip means the pin is more than one Kubernetes minor ahead of
	// the installed k3s. Control planes must upgrade one minor at a time, so
	// such a jump is refused; reinstalling on the new version is the
	// supported path.
	K3sMinorSkip bool

	BundleFrom string
	BundleTo   string
	// BundleDrifted means the recorded bundle does not match this
	// installer, or the stamped hash is missing (an interrupted converge);
	// ImageForced that a staged image tar demands a converge even at equal
	// versions, because the new image id must roll skalid.
	BundleDrifted bool
	ImageForced   bool
}

// PlanUpgrade derives the upgrade plan from a gathered status. imageTar
// reports whether the operator staged a skalid image tar.
func PlanUpgrade(status *Status, imageTar bool) UpgradePlan {
	plan := UpgradePlan{
		K3sFrom:  status.Host.K3sVersion,
		K3sTo:    K3sVersion,
		BundleTo: version.Version,
	}
	plan.K3sDrifted = !status.K3sCurrent
	if cmp, ok := compareK3sVersions(status.Host.K3sVersion, K3sVersion); ok {
		if cmp > 0 {
			plan.K3sDowngrade = true
		}
		if cmp < 0 {
			installed, _ := parseK3sVersion(status.Host.K3sVersion)
			pinned, _ := parseK3sVersion(K3sVersion)
			if pinned[0] != installed[0] || pinned[1]-installed[1] > 1 {
				plan.K3sMinorSkip = true
			}
		}
	}
	if status.Host.Record != nil && status.Host.Record.Node.Role == layout.RoleServer {
		plan.BundleFrom = status.BundleVersion
		plan.BundleDrifted = status.Initialized && !status.BundleCurrent
		plan.ImageForced = imageTar
	}
	return plan
}

// Nothing reports whether the plan changes anything for the given role;
// agents run no bundle, so only k3s drift counts there.
func (p UpgradePlan) Nothing(role string) bool {
	if role == layout.RoleAgent {
		return !p.K3sDrifted
	}
	return !p.K3sDrifted && !p.BundleDrifted && !p.ImageForced
}

// NodePullCredentialMissing reports whether this server's registries.yaml
// carries the managed-registry mirror but no node pull credential: the
// shape of a host installed before the registry required authentication.
// Such a host cannot re-run install, so upgrade is the migration path
// that heals it.
func NodePullCredentialMissing(ctx context.Context, runner host.Runner) (bool, error) {
	registries, err := runner.ReadFile(ctx, K3sRegistriesPath)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", K3sRegistriesPath, err)
	}
	if !strings.Contains(string(registries), bundle.RegistryInternalHost) {
		return false, fmt.Errorf("%s is missing the %s mirror; this host was not installed by skali",
			K3sRegistriesPath, bundle.RegistryInternalHost)
	}
	return registriesPullSecret(registries) == "", nil
}

// HealNodePullCredential mints the node pull credential and rewrites
// registries.yaml exactly as a fresh install would. containerd only reads
// the file at k3s startup, so when no k3s upgrade follows to restart the
// service (restart true), it is restarted here and waited healthy; the
// unit's KillMode=process keeps workload containers running through it.
// The converge that follows reads the credential back from registries.yaml
// and publishes it in-cluster, so skalid and the registry accept this node
// once token authentication turns on.
func HealNodePullCredential(ctx context.Context, runner host.Runner, restart bool, progress Progress) error {
	progress.Start("Mint registry pull credential")
	secret, err := newPullSecret()
	if err != nil {
		return err
	}
	if err := runner.WriteFile(ctx, K3sRegistriesPath, []byte(k3sRegistriesYAML(secret)), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", K3sRegistriesPath, err)
	}
	if !restart {
		return nil
	}
	progress.Start("Restart k3s to load the credential")
	result, err := runner.Run(ctx, host.Command{Name: "systemctl", Args: []string{"restart", "k3s"}})
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("systemctl restart k3s: exit %d: %s",
			result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	return waitK3sUpgraded(ctx, runner, layout.RoleServer, progress)
}

// UpgradeK3s re-runs the vendored install script under this installer's
// pin, waits until the upgraded k3s is back, and bumps the record's k3s
// version in memory only. Servers persist it through Init's SaveRecord so
// the canonical record, the in-cluster copy, and the bundle hash move
// together; a crash in between still reports truthfully because status
// probes the installed version rather than the record.
func UpgradeK3s(ctx context.Context, runner host.Runner, record *Record, progress Progress) error {
	role := record.Node.Role
	if err := upgradeK3s(ctx, runner, role, progress); err != nil {
		return err
	}
	if err := waitK3sUpgraded(ctx, runner, role, progress); err != nil {
		return err
	}
	record.Versions.K3s = K3sVersion
	return nil
}

// UpgradeNode upgrades a node that maintains no bundle: k3s only. Agents
// always land here, and so do secondary servers, whose bundle is
// maintained by the init owner. On these nodes the local record is no
// bundle-hash input, so it is saved directly.
func UpgradeNode(ctx context.Context, runner host.Runner, record *Record, progress Progress) error {
	if err := UpgradeK3s(ctx, runner, record, progress); err != nil {
		return err
	}
	record.Versions.Installer = version.Version
	return SaveRecord(ctx, runner, record)
}

// NodeUpgradeStep is one host in the ordered multi-node upgrade plan.
type NodeUpgradeStep struct {
	Name   string
	Role   string
	From   string
	IsSelf bool
}

// UpgradeSequence orders the per-host k3s upgrades from a gathered
// status: this host first when drifted, then the remaining servers
// name-sorted, then agents; nodes already on the pin are omitted. Purely
// informational: upgrades run per host, so the sequence is printed
// guidance, never remote execution.
func UpgradeSequence(status *Status) []NodeUpgradeStep {
	self := status.Host.Hostname
	var steps []NodeUpgradeStep
	add := func(node NodeStatus) {
		steps = append(steps, NodeUpgradeStep{
			Name: node.Name, Role: node.Role, From: node.K3sVersion, IsSelf: node.Name == self,
		})
	}
	for _, node := range status.Nodes {
		if node.Name == self && !node.Current {
			add(node)
		}
	}
	for _, node := range status.Nodes {
		if node.Name != self && node.Role == layout.RoleServer && !node.Current {
			add(node)
		}
	}
	for _, node := range status.Nodes {
		if node.Name != self && node.Role == layout.RoleAgent && !node.Current {
			add(node)
		}
	}
	return steps
}

// compareK3sVersions orders two k3s version strings such as v1.36.3+k3s1:
// numeric on major.minor.patch, then on the k3s packaging suffix. ok is
// false when either side does not parse; callers then skip ordering-based
// guards rather than misjudge.
func compareK3sVersions(a, b string) (int, bool) {
	left, okA := parseK3sVersion(a)
	right, okB := parseK3sVersion(b)
	if !okA || !okB {
		return 0, false
	}
	for i := range left {
		if left[i] != right[i] {
			if left[i] < right[i] {
				return -1, true
			}
			return 1, true
		}
	}
	return 0, true
}

func parseK3sVersion(v string) ([4]int, bool) {
	var parsed [4]int
	core, suffix, found := strings.Cut(strings.TrimPrefix(v, "v"), "+k3s")
	if !found {
		return parsed, false
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return parsed, false
	}
	for i, part := range parts {
		number, err := strconv.Atoi(part)
		if err != nil {
			return parsed, false
		}
		parsed[i] = number
	}
	number, err := strconv.Atoi(suffix)
	if err != nil {
		return parsed, false
	}
	parsed[3] = number
	return parsed, true
}
