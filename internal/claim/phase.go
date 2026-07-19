// Package claim defines the durable lifecycle of infrastructure claims:
// requested capabilities such as a logical database or a bucket that the
// platform places, provisions, and releases below product services. User and
// system claims traverse identical phases; only policy differs. Transient
// progress and failure detail belong to runs and health diagnostics, so there
// is no failed phase: level-triggered reconciliation keeps working toward the
// current phase's goal, and an unsatisfiable claim stays pending with a
// visible waiting condition. The bootstrap control-plane database has no
// claim and no phase; the substrate cannot select it.
package claim

import "github.com/Hinkolas/skali/internal/lifecycle"

type Phase string

const (
	// PhasePending: the claim is recorded and has no placement.
	PhasePending Phase = "pending"
	// PhaseBound: a placement assigns the claim to a cluster; tenant
	// provisioning and repair happen here. Revoking a placement before
	// provisioning returns the claim to pending.
	PhaseBound Phase = "bound"
	// PhaseProvisioned: tenant identity, credential reference, and connection
	// outputs are durably recorded, and consumers may bind. Relocation
	// supersedes the placement without changing the phase.
	PhaseProvisioned Phase = "provisioned"
	// PhaseReleasing: a persisted destructive decision started teardown. The
	// transition is one-way because data may already be destroyed.
	PhaseReleasing Phase = "releasing"
	// PhaseReleased: teardown finished; identity is retained for history. A
	// claim deleted before it was ever placed releases directly from pending.
	PhaseReleased Phase = "released"
)

// Phases is the claim lifecycle machine.
var Phases = lifecycle.Machine[Phase]{
	States: []Phase{PhasePending, PhaseBound, PhaseProvisioned, PhaseReleasing, PhaseReleased},
	Transitions: map[Phase][]Phase{
		PhasePending:     {PhaseBound, PhaseReleased},
		PhaseBound:       {PhaseProvisioned, PhasePending, PhaseReleasing},
		PhaseProvisioned: {PhaseReleasing},
		PhaseReleasing:   {PhaseReleased},
	},
}
