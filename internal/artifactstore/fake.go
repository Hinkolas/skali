package artifactstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/revision"
)

// Resolved is one resolver result: the durable record's id plus the
// revision-facing artifact document.
type Resolved struct {
	ArtifactID uuid.UUID
	Artifact   revision.Artifact
}

// Fake is the R1 artifact resolver: it produces deterministic digests from
// the application source instead of touching any registry, but drives the
// real pending -> verified (or abandoned) record lifecycle so tests
// exercise the same persistence the R3 resolvers will. Provenance marks
// every record as fake.
type Fake struct {
	Store     *Service
	ProjectID uuid.UUID
	// FailFor makes resolution fail for the named applications after the
	// pending record was created, exercising the abandoned path.
	FailFor map[string]error
}

// Resolve implements the deploy resolver seam.
func (f *Fake) Resolve(ctx context.Context, application string, source compiler.ApplicationSource) (Resolved, error) {
	kind := revision.KindImport
	upstream := source.Image.Literal()
	contextHash := ""
	if source.Kind == "build" {
		kind = revision.KindBuildLocal
		upstream = ""
		contextHash = deterministicHash("context", application, source)
	}
	record, err := f.Store.CreatePending(ctx, Pending{
		ProjectID:   f.ProjectID,
		Application: application,
		Kind:        kind,
		Upstream:    upstream,
		ContextHash: contextHash,
	})
	if err != nil {
		return Resolved{}, err
	}
	if failure, ok := f.FailFor[application]; ok {
		if err := f.Store.Abandon(ctx, record.ID); err != nil {
			return Resolved{}, err
		}
		return Resolved{}, fmt.Errorf("resolve %s: %w", application, failure)
	}

	digest := "sha256:" + deterministicHash("content", application, source)
	reference := "registry.local/skali/" + application
	provenance, err := json.Marshal(map[string]string{"resolver": "fake"})
	if err != nil {
		return Resolved{}, fmt.Errorf("artifactstore: encode provenance: %w", err)
	}
	if err := f.Store.Verify(ctx, record.ID, reference, digest, provenance); err != nil {
		return Resolved{}, err
	}
	resolved := Resolved{
		ArtifactID: record.ID,
		Artifact: revision.Artifact{
			Reference:   reference,
			Digest:      digest,
			Kind:        kind,
			Upstream:    upstream,
			ContextHash: contextHash,
		},
	}
	return resolved, nil
}

// deterministicHash derives a stable pseudo-digest from the application key
// and its source document, so unchanged inputs resolve to unchanged
// artifacts and revision dedup can be tested.
func deterministicHash(domain, application string, source compiler.ApplicationSource) string {
	payload, err := json.Marshal(struct {
		Domain      string                     `json:"domain"`
		Application string                     `json:"application"`
		Source      compiler.ApplicationSource `json:"source"`
	}{domain, application, source})
	if err != nil {
		// Marshalling a plain struct of strings cannot fail; keep the
		// signature clean for callers.
		panic(err)
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}
