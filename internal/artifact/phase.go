// Package artifact defines the durable lifecycle of managed-registry
// artifacts: the verified, content-addressed results of builds and imports.
// The build or import run carries progress and failure detail, so there is
// no failed phase; an artifact that never verifies is abandoned. Retention
// leases from revisions are relations, not phases: a leased artifact simply
// cannot be evicted.
package artifact

import "github.com/Hinkolas/skali/internal/lifecycle"

type Phase string

const (
	// PhasePending: the expected content of one build or import; a scoped
	// push credential may exist for it.
	PhasePending Phase = "pending"
	// PhaseVerified: the manifest and digest are confirmed present in the
	// managed registry. Only verified artifacts can enter a revision.
	PhaseVerified Phase = "verified"
	// PhaseAbandoned: the build or import ended without verified content;
	// partial uploads are cleaned up.
	PhaseAbandoned Phase = "abandoned"
	// PhaseEvicted: previously verified content was reclaimed by retention
	// policy. The record and its provenance remain for history; re-importing
	// the same upstream creates a new artifact record.
	PhaseEvicted Phase = "evicted"
)

// Phases is the artifact lifecycle machine.
var Phases = lifecycle.Machine[Phase]{
	States: []Phase{PhasePending, PhaseVerified, PhaseAbandoned, PhaseEvicted},
	Transitions: map[Phase][]Phase{
		PhasePending:  {PhaseVerified, PhaseAbandoned},
		PhaseVerified: {PhaseEvicted},
	},
}
