package observe

import (
	"github.com/google/uuid"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/module"
)

// claimGVK keys the substrate's synthetic claim projections. The group is
// skali's own: these objects exist in the observed store only, never in
// Kubernetes.
var claimGVK = schema.GroupVersionKind{Group: "claims.skali.dev", Version: "v1", Kind: "DatabaseClaim"}

// ClaimRef addresses one claim's synthetic projection.
func ClaimRef(claimID uuid.UUID) kube.ObjectRef {
	return kube.ObjectRef{GVK: claimGVK, Name: "claim-" + claimID.String()}
}

// ClaimObject builds the provider observation of one database claim
// (REWORK_V2 7.4). Service carries the dotted "databases.<key>" form so
// claim projections can never collide with an application of the same key.
func ClaimObject(environmentID uuid.UUID, service string, claimID uuid.UUID, status module.ClaimStatus) Object {
	return Object{
		Ref:         ClaimRef(claimID),
		Kind:        module.KindDatabaseClaim,
		Name:        service,
		Environment: environmentID,
		Service:     service,
		Claim:       &status,
	}
}
