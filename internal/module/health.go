package module

import "time"

// Health is the projected condition of one service, derived purely from
// prepared intent plus observed state (REWORK_V2 section 7.5).
type Health string

const (
	HealthUnknown     Health = "unknown"
	HealthProgressing Health = "progressing"
	HealthHealthy     Health = "healthy"
	HealthDegraded    Health = "degraded"
	HealthUnhealthy   Health = "unhealthy"
)

// Diagnostic is one structured observation attached to an evaluation.
type Diagnostic struct {
	Severity string `json:"severity"` // info | warning | error
	Code     string `json:"code"`
	Message  string `json:"message"`
	Resource string `json:"resource,omitempty"`
}

// Evaluation is the result of one pure health evaluation.
type Evaluation struct {
	Health      Health
	Diagnostics []Diagnostic
}

// ObservedResource kinds. The vocabulary is skali's, not Kubernetes': the
// ObservedStore translates cluster objects into these projections so module
// evaluation stays free of Kubernetes types.
const (
	KindSource     = "source"
	KindWorkload   = "workload"
	KindPod        = "pod"
	KindAutoscaler = "autoscaler"
	KindService    = "service"
	KindIngress    = "ingress"
	KindVolume     = "volume"
	// KindDatabaseClaim is the substrate's provider observation of one
	// claim's durable phase (REWORK_V2 7.4): published by the substrate
	// controller, not by a Kubernetes watch, so evaluation stays pure over
	// observed input.
	KindDatabaseClaim = "database-claim"
	// KindDatabaseCluster/KindDatabaseTenant project the CNPG Cluster and
	// Database objects through the dynamic CRD watch. Cluster projections
	// are platform-scoped and join service snapshots through the shared-key
	// mechanism.
	KindDatabaseCluster = "database-cluster"
	KindDatabaseTenant  = "database-tenant"
	// KindBucketClaim is the substrate's provider observation of one bucket
	// claim's durable phase, published like KindDatabaseClaim.
	KindBucketClaim = "bucket-claim"
	// KindObjectStore/KindBucket project the SeaweedFS system and per-bucket
	// usage through the poll-based provider observer (REWORK_V2 7.4). Store
	// projections are platform-scoped and join service snapshots through the
	// shared-key mechanism.
	KindObjectStore = "object-store"
	KindBucket      = "bucket"
)

// Observation source states. Anything but fresh means the projection may lag
// the cluster and modules must report unknown rather than guess.
const (
	SourceFresh   = "fresh"
	SourceStale   = "stale"
	SourceUnknown = "unknown"
)

// ObservedResource is one typed projection out of the R2 ObservedStore.
// Exactly one of the typed members matching Kind is set. Every snapshot
// handed to Evaluate begins with the KindSource pseudo-resource describing
// observation freshness; when its state is not fresh, modules return
// HealthUnknown with an observation_stale_since diagnostic instead of
// evaluating stale counts as truth.
type ObservedResource struct {
	Kind string
	Name string
	// Revision is the skali.dev/revision label value, empty when absent.
	// Pods never carry it: the label stays off pod templates so a new
	// revision does not roll every application.
	Revision string

	Source          *SourceStatus
	Workload        *WorkloadStatus
	Pod             *PodStatus
	Autoscaler      *AutoscalerStatus
	Claim           *ClaimStatus
	DatabaseCluster *DatabaseClusterStatus
	DatabaseTenant  *DatabaseTenantStatus
	ObjectStore     *ObjectStoreStatus
	Bucket          *BucketStatus
}

// SourceStatus describes the freshness of the observation source itself.
type SourceStatus struct {
	State      string    // SourceFresh | SourceStale | SourceUnknown
	StaleSince time.Time // zero while fresh
	LastSync   time.Time // zero before the first successful sync
}

// WorkloadStatus projects a Deployment-shaped workload. Desired is -1 when
// the field is autoscaler-owned and therefore absent from skali's intent.
type WorkloadStatus struct {
	Desired            int32
	Ready              int32
	Updated            int32
	Available          int32
	Generation         int64
	ObservedGeneration int64
	Conditions         []Condition
}

// PodStatus projects one pod: enough for topology and diagnostics without
// persisting pods anywhere.
type PodStatus struct {
	Phase    string
	Ready    bool
	Node     string
	Restarts int32
	Reason   string // e.g. CrashLoopBackOff, ImagePullBackOff, Unschedulable
	Message  string
	Started  time.Time
}

// AutoscalerStatus projects a HorizontalPodAutoscaler.
type AutoscalerStatus struct {
	Min             int32
	Max             int32
	Current         int32
	DesiredReplicas int32
}

// ClaimStatus projects one infrastructure claim's durable phase plus the
// current waiting reason while it is not provisioned. Phases follow
// internal/claim.
type ClaimStatus struct {
	Phase   string
	Waiting string
}

// DatabaseClusterStatus projects one CNPG pool: desired and ready
// instances, the operator's phase, and the current primary.
type DatabaseClusterStatus struct {
	Instances      int32
	ReadyInstances int32
	Phase          string
	Primary        string
}

// DatabaseTenantStatus projects one CNPG Database object: whether the
// operator reconciled it, its message when it did not, and the pool it
// lives on (matching the pool projection's Name).
type DatabaseTenantStatus struct {
	Applied bool
	Message string
	Pool    string
}

// ObjectStoreStatus projects the SeaweedFS system: desired and ready
// component counts plus gateway health.
type ObjectStoreStatus struct {
	MastersDesired       int32
	MastersReady         int32
	VolumeServersDesired int32
	VolumeServersReady   int32
	FilerReady           bool
	S3Ready              bool
}

// BucketStatus projects one bucket's existence and usage as reported by the
// provider observer. Usage is approximate by up to one poll interval.
type BucketStatus struct {
	Exists      bool
	UsedBytes   int64
	ObjectCount int64
	QuotaBytes  int64
	ReadOnly    bool
}

// Condition is one status condition, provider-agnostic.
type Condition struct {
	Type    string
	Status  string
	Reason  string
	Message string
}

// StaleSource returns the leading source pseudo-resource when it reports
// anything but fresh, so modules share one guard: evaluate nothing on a
// stale view. Snapshots order the kubernetes source first, so this guard
// covers cluster observation; provider sources are opted into by name via
// SourceNamed.
func StaleSource(observed []ObservedResource) *SourceStatus {
	for _, resource := range observed {
		if resource.Kind != KindSource || resource.Source == nil {
			continue
		}
		if resource.Source.State != SourceFresh {
			return resource.Source
		}
		return nil
	}
	// No source resource at all: the snapshot's provenance is unknown.
	return &SourceStatus{State: SourceUnknown}
}

// SourceNamed returns one named source's status from a snapshot, or nil when
// the source is not registered. Modules that consume a provider observer
// (e.g. the bucket module and "seaweedfs") select it explicitly so one
// provider's staleness never blocks another module's evaluation.
func SourceNamed(observed []ObservedResource, name string) *SourceStatus {
	for _, resource := range observed {
		if resource.Kind == KindSource && resource.Name == name {
			return resource.Source
		}
	}
	return nil
}
