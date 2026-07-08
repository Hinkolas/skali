package engine

import (
	"fmt"
	"strings"
)

// The skali label taxonomy. Containers are self-describing: labels alone
// carry ownership, management kind, and (later) resource identity, so any
// daemon restart can rebuild "what runs here and what it belongs to" without
// node-local state.
const (
	// LabelManaged marks a container as skali-owned ("true"). skali never
	// touches containers without it.
	LabelManaged = "skali.managed"

	// LabelKind says how a container is managed. Applications belong to a
	// project directly; a database container is an engine *pool* that may
	// host logical databases of many projects (so it never carries a project
	// label); system containers are skali's own infrastructure.
	LabelKind = "skali.kind"
)

// LabelKind values.
const (
	KindApplication = "application"
	KindDatabase    = "database"
	KindSystem      = "system"
)

// Orchestration labels: how the reconciler recognizes its containers from
// observed state alone (adopt, match, GC) — nothing about ownership lives
// only in memory, so a master failover loses nothing.
const (
	// LabelWorkload is the owning workload's UUID.
	LabelWorkload = "skali.workload"

	// LabelInstance is the replica ordinal within its workload.
	LabelInstance = "skali.instance"

	// LabelConfigHash fingerprints everything the running container was
	// created from; a mismatch against the freshly materialized spec means
	// "replace".
	LabelConfigHash = "skali.config-hash"
)

// Reserved identity labels. Documented now so the taxonomy is complete;
// their semantics arrive with the layers that own them.
const (
	// Application instances (application layer):
	LabelProject     = "skali.project"
	LabelEnvironment = "skali.environment"
	LabelRelease     = "skali.release"

	// Database pools (database layer):
	LabelPool = "skali.pool"

	// System components (traefik-edge, traefik-node, registry,
	// controlplane-db, ...):
	LabelComponent = "skali.component"
)

// labelPrefix is the namespace reserved for skali itself.
const labelPrefix = "skali."

// Kinds lists the valid LabelKind values.
var Kinds = []string{KindApplication, KindDatabase, KindSystem}

// ValidateKind checks a LabelKind value. There is no default kind: every
// creator must say what it is creating.
func ValidateKind(kind string) error {
	switch kind {
	case KindApplication, KindDatabase, KindSystem:
		return nil
	case "":
		return fmt.Errorf("engine: missing %s label (one of %s)", LabelKind, strings.Join(Kinds, ", "))
	default:
		return fmt.Errorf("engine: invalid %s label %q (one of %s)", LabelKind, kind, strings.Join(Kinds, ", "))
	}
}

// ValidateUserLabels rejects caller-supplied labels that squat on the skali
// namespace — identity labels are stamped by skali, never passed through.
func ValidateUserLabels(labels map[string]string) error {
	for k := range labels {
		if strings.HasPrefix(k, labelPrefix) {
			return fmt.Errorf("engine: label %q uses the reserved %q prefix", k, labelPrefix)
		}
	}
	return nil
}
