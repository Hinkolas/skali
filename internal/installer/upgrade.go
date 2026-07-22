package installer

import (
	"context"
	"strconv"
	"strings"

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
	if cmp, ok := compareK3sVersions(status.Host.K3sVersion, K3sVersion); ok && cmp > 0 {
		plan.K3sDowngrade = true
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

// UpgradeAgent upgrades an agent node: k3s only, since agents run no
// bundle. The agent record is host-local and no bundle-hash input, so it
// is saved directly.
func UpgradeAgent(ctx context.Context, runner host.Runner, record *Record, progress Progress) error {
	if err := UpgradeK3s(ctx, runner, record, progress); err != nil {
		return err
	}
	record.Versions.Installer = version.Version
	return SaveRecord(ctx, runner, record)
}

// compareK3sVersions orders two k3s version strings such as v1.33.3+k3s1:
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
