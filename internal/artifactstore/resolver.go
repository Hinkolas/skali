package artifactstore

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/Hinkolas/skali/internal/artifact"
	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/revision"
)

// RecordResolver resolves applications from already-verified artifact rows.
// The R3 deployment flow builds, imports, and verifies every artifact
// before revision preparation runs, so resolution is a pure record read: no
// registry contact, no digest inference. Kind and source consistency are
// re-checked by revision.Build.
type RecordResolver struct {
	Store *Service
	// IDs maps application keys to their verified artifact records.
	IDs map[string]uuid.UUID
}

// Resolve implements the deploy resolver seam.
func (r *RecordResolver) Resolve(ctx context.Context, application string, source compiler.ApplicationSource) (Resolved, error) {
	id, ok := r.IDs[application]
	if !ok {
		return Resolved{}, fmt.Errorf("artifactstore: no artifact recorded for application %s", application)
	}
	row, err := r.Store.Get(ctx, id)
	if err != nil {
		return Resolved{}, err
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
