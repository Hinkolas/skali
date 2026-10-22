package observe

import (
	"github.com/google/uuid"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/module"
)

// claimGVK and bucketClaimGVK key the substrate's synthetic claim
// projections. The group is skali's own: these objects exist in the
// observed store only, never in Kubernetes.
var (
	claimGVK       = schema.GroupVersionKind{Group: "claims.skali.dev", Version: "v1", Kind: "DatabaseClaim"}
	bucketClaimGVK = schema.GroupVersionKind{Group: "claims.skali.dev", Version: "v1", Kind: "BucketClaim"}
)

// DatabaseConnectionsGVK keys the substrate's synthetic connection check of
// a pool, which exists in the observed store only.
var DatabaseConnectionsGVK = schema.GroupVersionKind{Group: "claims.skali.dev", Version: "v1", Kind: "PoolConnections"}

// DatabaseConnectionsObject builds the platform-scoped connection check of
// one pool. It carries the pool name as its shared key, like the pool's
// Cluster, so every environment with a database on the pool reads it.
func DatabaseConnectionsObject(pool string, status module.DatabaseConnectionsStatus) Object {
	return Object{
		Ref:                 kube.ObjectRef{GVK: DatabaseConnectionsGVK, Name: pool},
		Kind:                module.KindDatabaseConnections,
		Name:                pool,
		SharedKey:           pool,
		DatabaseConnections: &status,
	}
}

// ClaimRef addresses one database claim's synthetic projection.
func ClaimRef(claimID uuid.UUID) kube.ObjectRef {
	return kube.ObjectRef{GVK: claimGVK, Name: "claim-" + claimID.String()}
}

// ClaimObject builds the provider observation of one database claim.
// Service carries the dotted "databases.<key>" form so
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

// BucketClaimRef addresses one bucket claim's synthetic projection.
func BucketClaimRef(claimID uuid.UUID) kube.ObjectRef {
	return kube.ObjectRef{GVK: bucketClaimGVK, Name: "claim-" + claimID.String()}
}

// BucketClaimObject builds the provider observation of one bucket claim;
// service carries the dotted "buckets.<key>" form.
func BucketClaimObject(environmentID uuid.UUID, service string, claimID uuid.UUID, status module.ClaimStatus) Object {
	return Object{
		Ref:         BucketClaimRef(claimID),
		Kind:        module.KindBucketClaim,
		Name:        service,
		Environment: environmentID,
		Service:     service,
		Claim:       &status,
	}
}
