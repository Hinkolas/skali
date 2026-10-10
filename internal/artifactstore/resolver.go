package artifactstore

import (
	"context"
	"fmt"

	"github.com/Hinkolas/skali/internal/artifact"
	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/revision"
	"github.com/Hinkolas/skali/internal/store"
)

// RecordResolver resolves applications from already-verified artifact rows.
// The deployment flow builds, imports, and verifies every artifact
// before revision preparation runs, and completion reads the rows to check
// that, so resolution is a pure record lookup: no read, no registry
// contact, no digest inference. Kind and source consistency are re-checked
// by revision.Build.
type RecordResolver struct {
	// Records maps application keys to their verified artifact rows.
	Records map[string]*store.Artifact
}

// Resolve implements the deploy resolver seam.
func (r *RecordResolver) Resolve(_ context.Context, application string, source compiler.ApplicationSource) (Resolved, error) {
	row, ok := r.Records[application]
	if !ok {
		return Resolved{}, fmt.Errorf("artifactstore: no artifact recorded for application %s", application)
	}
	if artifact.Phase(row.Phase) != artifact.PhaseVerified {
		return Resolved{}, fmt.Errorf("artifactstore: artifact for application %s is %s, not verified", application, row.Phase)
	}
	if row.Digest == nil {
		return Resolved{}, fmt.Errorf("artifactstore: artifact for application %s carries no digest", application)
	}
	return Resolved{
		ArtifactID: row.ID,
		Artifact: revision.Artifact{
			Reference:   row.Reference,
			Digest:      *row.Digest,
			Kind:        row.Kind,
			Upstream:    row.Upstream,
			ContextHash: row.ContextHash,
		},
	}, nil
}
