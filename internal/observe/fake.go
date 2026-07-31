package observe

import (
	"github.com/google/uuid"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/module"
)

// Fake is the test implementation of the observed store: the identical read
// contract with scripted mutators instead of informers. Reconcile and
// module tests drive it directly.
type Fake struct {
	*Store
}

func NewFake() *Fake {
	return &Fake{Store: NewStore(nil)}
}

// SetFresh marks every registered source synced and fresh, the baseline of
// most tests.
func (f *Fake) SetFresh() {
	for _, source := range f.Sources() {
		f.MarkReady(source.Name)
	}
}

// SetStale forces the kubernetes source stale through the real transition
// path.
func (f *Fake) SetStale() {
	f.SetSourceStale(SourceKubernetes)
}

// SetSourceFresh marks one named source synced and fresh, registering it if
// needed.
func (f *Fake) SetSourceFresh(source string) {
	f.MarkReady(source)
}

// SetSourceStale forces one named source stale through the real transition
// path, registering it if needed.
func (f *Fake) SetSourceStale(source string) {
	f.MarkReady(source)
	f.MarkFailure(source)
	f.EvaluateFreshness(source, 0)
}

// SetWorkload records a deployment-shaped workload for a service. The
// object name is explicit because pruning compares it against the rendered
// desired set.
func (f *Fake) SetWorkload(environmentID uuid.UUID, namespace, objectName, service, revision string, status module.WorkloadStatus) {
	f.Upsert(Object{
		Ref: kube.ObjectRef{
			GVK:       schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"},
			Namespace: namespace,
			Name:      objectName,
		},
		Kind:        module.KindWorkload,
		Name:        service,
		Environment: environmentID,
		Service:     service,
		Revision:    revision,
		Workload:    &status,
	})
}

// SetPod records one pod of a service.
func (f *Fake) SetPod(environmentID uuid.UUID, namespace, service, podName, node string, status module.PodStatus) {
	pod := status
	pod.Node = node
	f.Upsert(Object{
		Ref: kube.ObjectRef{
			GVK:       schema.GroupVersionKind{Version: "v1", Kind: "Pod"},
			Namespace: namespace,
			Name:      podName,
		},
		Kind:        module.KindPod,
		Name:        podName,
		Environment: environmentID,
		Service:     service,
		Node:        node,
		Pod:         &pod,
	})
}

// SetReleaseJob records one application's release Job projection. The
// object name must match the rendered per-revision Job name; service is the
// application's bare key (the Job object carries the service identity, its
// pods do not).
func (f *Fake) SetReleaseJob(environmentID uuid.UUID, namespace, objectName, service string, status JobStatus) {
	f.Upsert(Object{
		Ref: kube.ObjectRef{
			GVK:       schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"},
			Namespace: namespace,
			Name:      objectName,
		},
		Kind:        KindReleaseJob,
		Name:        objectName,
		Environment: environmentID,
		Service:     service,
		Job:         &status,
	})
}

// SetAutoscaler records the autoscaler of a service.
func (f *Fake) SetAutoscaler(environmentID uuid.UUID, namespace, objectName, service string, status module.AutoscalerStatus) {
	f.Upsert(Object{
		Ref: kube.ObjectRef{
			GVK:       schema.GroupVersionKind{Group: "autoscaling", Version: "v2", Kind: "HorizontalPodAutoscaler"},
			Namespace: namespace,
			Name:      objectName,
		},
		Kind:        module.KindAutoscaler,
		Name:        service,
		Environment: environmentID,
		Service:     service,
		Autoscaler:  &status,
	})
}

// SetDatabaseClaim records the substrate's claim projection for a database
// service; service uses the dotted "databases.<key>" form.
func (f *Fake) SetDatabaseClaim(environmentID uuid.UUID, service string, claimID uuid.UUID, status module.ClaimStatus) {
	f.Upsert(ClaimObject(environmentID, service, claimID, status))
}

// SetDatabaseTenant records the CNPG Database projection of one service's
// tenant; service uses the dotted form and pool links the shared pool.
func (f *Fake) SetDatabaseTenant(environmentID uuid.UUID, service, pool, databaseName string, status module.DatabaseTenantStatus) {
	status.Pool = pool
	f.Upsert(Object{
		Ref: kube.ObjectRef{
			GVK:       schema.GroupVersionKind{Group: "postgresql.cnpg.io", Version: "v1", Kind: "Database"},
			Namespace: "skali-platform",
			Name:      databaseName,
		},
		Kind:           module.KindDatabaseTenant,
		Name:           databaseName,
		Environment:    environmentID,
		Service:        service,
		SharedKey:      pool,
		DatabaseTenant: &status,
	})
}

// SetDatabasePool records the platform-scoped CNPG Cluster projection.
func (f *Fake) SetDatabasePool(name string, status module.DatabaseClusterStatus) {
	f.Upsert(Object{
		Ref: kube.ObjectRef{
			GVK:       schema.GroupVersionKind{Group: "postgresql.cnpg.io", Version: "v1", Kind: "Cluster"},
			Namespace: "skali-platform",
			Name:      name,
		},
		Kind:            module.KindDatabaseCluster,
		Name:            name,
		SharedKey:       name,
		DatabaseCluster: &status,
	})
}

// SetBucketClaim records the substrate's claim projection for a bucket
// service; service uses the dotted "buckets.<key>" form.
func (f *Fake) SetBucketClaim(environmentID uuid.UUID, service string, claimID uuid.UUID, status module.ClaimStatus) {
	f.Upsert(BucketClaimObject(environmentID, service, claimID, status))
}

// SetBucketUsage records the provider observation of one bucket's existence
// and usage; service uses the dotted form and store links the platform
// object-store projection.
func (f *Fake) SetBucketUsage(environmentID uuid.UUID, service, storeKey, bucketName string, status module.BucketStatus) {
	f.Upsert(Object{
		Ref: kube.ObjectRef{
			GVK:  schema.GroupVersionKind{Group: "seaweed.skali.dev", Version: "v1", Kind: "Bucket"},
			Name: bucketName,
		},
		Kind:        module.KindBucket,
		Name:        service,
		Environment: environmentID,
		Service:     service,
		SharedKey:   storeKey,
		Source:      "seaweedfs",
		Bucket:      &status,
	})
}

// SetObjectStore records the platform-scoped SeaweedFS system projection.
func (f *Fake) SetObjectStore(name string, status module.ObjectStoreStatus) {
	f.Upsert(Object{
		Ref: kube.ObjectRef{
			GVK:  schema.GroupVersionKind{Group: "seaweed.skali.dev", Version: "v1", Kind: "ObjectStore"},
			Name: name,
		},
		Kind:        module.KindObjectStore,
		Name:        name,
		SharedKey:   "objectstore/" + name,
		Source:      "seaweedfs",
		ObjectStore: &status,
	})
}
