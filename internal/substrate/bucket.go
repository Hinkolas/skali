package substrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Hinkolas/skali/internal/claim"
	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/observe"
	"github.com/Hinkolas/skali/internal/platform"
	"github.com/Hinkolas/skali/internal/reconcile"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/substrate/seaweed"
	"github.com/Hinkolas/skali/internal/utils"
)

// reconcileBucketClaim drives one bucket claim toward its phase goal.
// Waiting states requeue with a visible reason instead of failing; there is
// no failed phase.
func (c *Controller) reconcileBucketClaim(ctx context.Context, id uuid.UUID) (time.Duration, error) {
	row, err := c.deps.DB.GetBucketClaim(ctx, id)
	if err != nil {
		if errors.Is(err, dbstore.ErrNotFound) {
			return 0, nil
		}
		return 0, err
	}

	switch claim.Phase(row.Phase) {
	case claim.PhaseReleased:
		c.setWaiting(id, "")
		c.publishBucketClaim(*row)
		return 0, nil
	case claim.PhaseReleasing:
		return c.teardownBucketClaim(ctx, *row)
	}

	transitioned, err := c.provisionBucket(ctx, *row)
	requeue := time.Duration(0)
	switch waiting, ok := errors.AsType[errWaiting](err); {
	case ok:
		c.setWaiting(id, waiting.reason)
		requeue = requeueWait
	case err != nil:
		return 0, err
	default:
		c.setWaiting(id, "")
	}

	// Publish the fresh phase before poking the environment so its next
	// pass evaluates against the new truth.
	current, err := c.deps.DB.GetBucketClaim(ctx, id)
	if err != nil {
		return 0, err
	}
	c.publishBucketClaim(*current)
	if (transitioned || current.Phase != row.Phase) && c.deps.Enqueue != nil && current.EnvironmentID != nil {
		c.deps.Enqueue(*current.EnvironmentID)
	}
	// A non-terminal claim must never leave the queue: a pass that ends
	// without an error or a wait still owes the next step a wakeup.
	if requeue == 0 {
		switch claim.Phase(current.Phase) {
		case claim.PhaseProvisioned, claim.PhaseReleased:
		default:
			requeue = requeueWait
		}
	}
	return requeue, nil
}

// provisionBucket walks a pending/bound/provisioned claim through the store
// gate, allocation, credentials, the external bucket + identity ensures,
// and the output mirror. Every step is idempotent, so provisioned claims
// re-run it as drift repair. It reports whether the claim reached
// provisioned in this pass.
func (c *Controller) provisionBucket(ctx context.Context, row store.BucketClaim) (bool, error) {
	sw, err := c.ensureObjectStoreRow(ctx)
	if err != nil {
		return false, err
	}
	ready, reason, err := c.objectStoreReady(ctx, *sw)
	if err != nil {
		return false, err
	}
	if !ready {
		// The store's own pass brings it up; a ready store keeps its own
		// cadence, and this pass applies the S3 access policy itself.
		c.EnqueueObjectStore(reasonBucket)
		if reason == "" {
			reason = "waiting for the object store"
		}
		return false, errWaiting{reason: reason}
	}
	if c.deps.Seaweed == nil {
		return false, errWaiting{reason: "the object-storage substrate is not available"}
	}

	allocation, err := c.ensureAllocationRecord(ctx, row, sw)
	if err != nil {
		return false, err
	}
	// The allocation makes the environment a holder: admit its namespace to
	// the S3 port before any output tells a workload to connect. (The store
	// pass enqueued above listed holders before this allocation existed.)
	if err := c.ensureS3Access(ctx, sw.ID); err != nil {
		return false, err
	}
	// The published endpoint follows the claim's route: gaining,
	// changing, or losing it republishes the endpoint. The row records it
	// only after the mirror holds it (publishBucketOutputs), because the
	// row is what rolls the consumers.
	endpoint, err := c.bucketEndpoint(row)
	if err != nil {
		return false, err
	}
	credential, err := c.ensureBucketCredentialSecret(ctx, row, *allocation)
	if err != nil {
		return false, err
	}
	now := time.Now()
	if err := c.deps.Seaweed.EnsureBucket(ctx, allocation.BucketName); err != nil {
		return false, err
	}
	// A fenced bucket (a restore rewriting it) keeps its identity absent
	// until the fence is lifted; the platform identity alone writes. The
	// key set comes from the Secret alone (desiredCredentials): the current
	// pair, plus a rotation's previous pair inside its overlap window.
	if allocation.FencedAt == nil {
		if err := c.deps.Seaweed.EnsureIdentity(ctx, seaweed.Identity{
			Name:        allocation.BucketName,
			Credentials: desiredCredentials(credential, now),
			Actions:     seaweed.BucketActions(allocation.BucketName),
		}); err != nil {
			return false, err
		}
	}
	// The settings Skali owns on the bucket (the policy that keeps the
	// identity out of bucket administration, the declared CORS or none,
	// no lifecycle, no versioning) converge here and on every probe pass.
	desiredCORS, err := seaweed.CORSConfig(row.Cors)
	if err != nil {
		return false, err
	}
	repaired, err := c.deps.Seaweed.EnsureBucketConfiguration(ctx, allocation.BucketName,
		seaweed.BucketPolicy(allocation.BucketName), desiredCORS)
	if errors.Is(err, seaweed.ErrNoPlatformCredentials) {
		// The store's reconcile loads the platform keypair; a claim that
		// gets here first (a fresh process) visibly waits one pass.
		return false, errWaiting{reason: "waiting for the platform identity"}
	}
	if err != nil {
		return false, err
	}
	if len(repaired) > 0 {
		slog.Info("substrate: bucket configuration reset", "bucket", allocation.BucketName, "settings", repaired)
	}
	if err := c.settleBucketOutputs(ctx, row, allocation, credential, endpoint, now); err != nil {
		return false, err
	}

	// The allocation may have bound the claim mid-pass; the transition test
	// needs the fresh phase or the pass that binds and completes can never
	// settle.
	current, err := c.deps.DB.GetBucketClaim(ctx, row.ID)
	if err != nil {
		return false, err
	}
	if claim.Phase(current.Phase) == claim.PhaseBound {
		if _, err := c.deps.DB.TransitionBucketClaim(ctx, row.ID, claim.PhaseProvisioned); err != nil {
			return false, err
		}
		c.pokeProbe()
		return true, nil
	}
	return false, nil
}

// ensureAllocationRecord generates and durably records the bucket identity
// once; the allocation binds the claim (it is the placement).
func (c *Controller) ensureAllocationRecord(ctx context.Context, row store.BucketClaim, sw *store.ObjectStore) (*store.BucketAllocation, error) {
	endpoint, err := c.bucketEndpoint(row)
	if err != nil {
		return nil, err
	}
	return c.deps.DB.RecordAllocation(ctx, dbstore.AllocationInput{
		ClaimID:          row.ID,
		StoreID:          sw.ID,
		BucketName:       "b-" + dnsName(bucketOwnerBase(row)) + "-" + utils.ShortID(row.ID),
		AccessKeyID:      seaweed.GenerateAccessKey(),
		CredentialSecret: "s3cred-" + utils.ShortID(row.ID),
		Endpoint:         endpoint,
		Region:           seaweed.Region,
	})
}

// bucketEndpoint is the endpoint published to consumers: the bucket's own
// route when the claim records one (the environment renders the edge for
// it), else the in-cluster service URL. A bucket without a route is
// reachable from inside the cluster only, like a database.
func (c *Controller) bucketEndpoint(row store.BucketClaim) (string, error) {
	if len(row.Route) > 0 {
		var route reconcile.BucketRoute
		if err := json.Unmarshal(row.Route, &route); err != nil {
			return "", fmt.Errorf("substrate: decode route of bucket claim %s: %w", row.ID, err)
		}
		if route.Domain != "" {
			return route.Endpoint(), nil
		}
	}
	return InternalBucketEndpoint(), nil
}

// InternalBucketEndpoint is the in-cluster S3 gateway URL, published as the
// internal_endpoint output beside endpoint so backend traffic need not
// hairpin through the edge while signed URLs carry the route's host. With one store per installation it is a constant.
func InternalBucketEndpoint() string {
	return platform.InternalS3Endpoint()
}

// ensureBucketCredentialSecret creates the claim's keypair Secret on first
// provisioning and returns the Secret as it is: the one source of truth
// for the keys the identity accepts (see rotation.go). Secret keys exist
// only in Secrets; they are never logged or persisted elsewhere.
func (c *Controller) ensureBucketCredentialSecret(ctx context.Context, row store.BucketClaim, allocation store.BucketAllocation) (*corev1.Secret, error) {
	existing, err := c.deps.Cluster.GetSecret(ctx, Namespace, allocation.CredentialSecret)
	if err == nil {
		return existing, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("substrate: read bucket credential secret: %w", err)
	}
	secret := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      allocation.CredentialSecret,
			Namespace: Namespace,
			Labels:    bucketClaimLabels(row),
		},
		Type: corev1.SecretTypeOpaque,
		StringData: map[string]string{
			credentialAccessKey: allocation.AccessKeyID,
			credentialSecretKey: seaweed.GenerateSecretKey(),
		},
	}
	if _, err := c.deps.Cluster.ApplyAs(ctx, secret, kube.FieldManagerPlatform, false); err != nil {
		return nil, fmt.Errorf("substrate: apply bucket credential secret: %w", err)
	}
	created, err := c.deps.Cluster.GetSecret(ctx, Namespace, allocation.CredentialSecret)
	if err != nil {
		return nil, fmt.Errorf("substrate: read bucket credential secret: %w", err)
	}
	return created, nil
}

// settleBucketOutputs brings the consumers' view of the bucket up to date
// once the identity holds the Secret's keys: the output mirror, the
// rotation bookkeeping, then the publication. Rotation bookkeeping (commit
// a new key to the row, retire a previous pair whose instant passed) runs
// before the publication so that a rotation's two bumps, the credential
// version and the output version of the rewritten mirror, reach the
// consumers as one roll: the publication wakes the environment after both.
// Only a pass that commits a key the mirror already held (one interrupted
// after writing it) has no publication to do that, so the commit wakes the
// environment itself.
func (c *Controller) settleBucketOutputs(ctx context.Context, row store.BucketClaim, allocation *store.BucketAllocation,
	credential *corev1.Secret, endpoint string, now time.Time) error {
	pair := currentCredential(credential)
	changed, err := c.ensureBucketOutputMirror(ctx, row, *allocation, endpoint, pair.AccessKey, pair.SecretKey)
	if err != nil {
		return err
	}
	if err := c.reconcileCredentialRotation(ctx, row, allocation, credential, now, !changed); err != nil {
		return err
	}
	return c.publishBucketOutputs(ctx, row, allocation, endpoint, changed)
}

// publishBucketOutputs records on the allocation what the output mirror
// now holds, once it holds it. The output version advances when the mirror
// changed or the endpoint moved, and the environment is woken so its
// consumers roll onto the values they will now read; rolling them before
// the mirror changed would start pods on the old values and never roll
// them again.
func (c *Controller) publishBucketOutputs(ctx context.Context, row store.BucketClaim, allocation *store.BucketAllocation, endpoint string, changed bool) error {
	if !changed && allocation.Endpoint == endpoint && allocation.OutputsPublishedAt != nil {
		return nil
	}
	published, err := c.deps.DB.PublishAllocationOutputs(ctx, allocation.ID, endpoint, changed)
	if err != nil {
		return err
	}
	if published.OutputVersion != allocation.OutputVersion {
		slog.Info("substrate: bucket outputs republished", "bucket", published.BucketName,
			"endpoint", published.Endpoint, "outputVersion", published.OutputVersion)
		if c.deps.Enqueue != nil && row.EnvironmentID != nil {
			c.deps.Enqueue(*row.EnvironmentID)
		}
	}
	*allocation = *published
	return nil
}

// ensureBucketOutputMirror writes the service claim's connection outputs
// into its environment namespace and reports whether that changed the
// Secret; the six keys mirror the compiler's bucket output catalog. System
// claims publish outputs through the internal claim API instead.
func (c *Controller) ensureBucketOutputMirror(ctx context.Context, row store.BucketClaim, allocation store.BucketAllocation, endpoint, accessKey, secretKey string) (bool, error) {
	if row.OwnerKind != dbstore.OwnerService {
		return false, nil
	}
	project, environment, service, ok := ownerNames(row.OwnerRef)
	if !ok {
		return false, fmt.Errorf("substrate: malformed owner ref %q", row.OwnerRef)
	}
	environmentID := ""
	if row.EnvironmentID != nil {
		environmentID = row.EnvironmentID.String()
	}
	secret := kubernetes.RenderOutputSecret(project, environment, environmentID,
		"buckets", service, map[string][]byte{
			"endpoint":          []byte(endpoint),
			"internal_endpoint": []byte(InternalBucketEndpoint()),
			"name":              []byte(allocation.BucketName),
			"region":            []byte(allocation.Region),
			"access_key":        []byte(accessKey),
			"secret_key":        []byte(secretKey),
		})
	result, err := c.deps.Cluster.ApplyAs(ctx, secret, kube.FieldManagerPlatform, false)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// The environment namespace is created by the environment
			// reconciler; until it exists the claim visibly waits.
			return false, errWaiting{reason: "environment namespace not created yet"}
		}
		return false, fmt.Errorf("substrate: apply bucket output mirror: %w", err)
	}
	return result.Changed, nil
}

// teardownBucketClaim executes the persisted destructive decision: identity
// first (no new writes), then the bucket metadata, then the collection (the
// volume files themselves), then the Secrets, then the durable release.
// The DeleteBucket -> DeleteCollection ordering is load-bearing (measured:
// concurrent per-chunk deletes race the collection drop and a volume server
// re-announces a half-deleted volume).
func (c *Controller) teardownBucketClaim(ctx context.Context, row store.BucketClaim) (time.Duration, error) {
	allocation, err := c.deps.DB.LiveAllocation(ctx, row.ID)
	switch {
	case errors.Is(err, dbstore.ErrNotFound):
		return 0, c.finishBucketClaimRelease(ctx, row)
	case err != nil:
		return 0, err
	}

	// External deletes need the store running; a released store took the
	// data with it.
	sw, err := c.deps.DB.LiveObjectStore(ctx)
	if err == nil && sw.State != dbstore.StateReleasing {
		ready, reason, err := c.objectStoreReady(ctx, *sw)
		if err != nil {
			return 0, err
		}
		if !ready {
			if reason == "" {
				reason = "waiting for the object store"
			}
			c.setWaiting(row.ID, reason+"; the bucket releases once it serves")
			return requeueWait, nil
		}
		if c.deps.Seaweed == nil {
			c.setWaiting(row.ID, "the object-storage substrate is not available")
			return requeueWait, nil
		}
		if err := c.deps.Seaweed.DeleteIdentity(ctx, allocation.BucketName); err != nil {
			return 0, err
		}
		if err := c.deps.Seaweed.DeleteBucket(ctx, allocation.BucketName); err != nil {
			return 0, err
		}
		if err := c.deps.Seaweed.DeleteCollection(ctx, allocation.BucketName); err != nil {
			return 0, err
		}
	} else if err != nil && !errors.Is(err, dbstore.ErrNotFound) {
		return 0, err
	}

	if _, err := c.deps.Cluster.Delete(ctx, kube.ObjectRef{
		GVK: secretGVK, Namespace: Namespace, Name: allocation.CredentialSecret,
	}); err != nil {
		return 0, err
	}
	if row.OwnerKind == dbstore.OwnerService {
		if _, _, service, ok := ownerNames(row.OwnerRef); ok && row.EnvironmentID != nil {
			if _, err := c.deps.Cluster.Delete(ctx, kube.ObjectRef{
				GVK:       secretGVK,
				Namespace: kubernetes.NamespaceName(row.EnvironmentID.String()),
				Name:      kubernetes.OutputSecretName("buckets", service),
			}); err != nil {
				return 0, err
			}
		}
	}
	return 0, c.finishBucketClaimRelease(ctx, row)
}

func (c *Controller) finishBucketClaimRelease(ctx context.Context, row store.BucketClaim) error {
	if err := c.deps.DB.CompleteBucketClaimRelease(ctx, row.ID); err != nil {
		return err
	}
	// The environment stops being a holder: close the S3 port to it.
	if err := c.ensureS3AccessForLiveStore(ctx); err != nil {
		return err
	}
	c.pokeProbe()
	c.setWaiting(row.ID, "")
	if c.deps.Observed != nil {
		c.deps.Observed.Remove(observe.BucketClaimRef(row.ID))
	}
	if c.deps.Enqueue != nil && row.EnvironmentID != nil {
		c.deps.Enqueue(*row.EnvironmentID)
	}
	return nil
}

// publishBucketClaim upserts the claim's provider observation so health
// evaluation stays pure over observed input; released claims leave the
// store.
func (c *Controller) publishBucketClaim(row store.BucketClaim) {
	if c.deps.Observed == nil || row.OwnerKind != dbstore.OwnerService || row.EnvironmentID == nil {
		return
	}
	if claim.Phase(row.Phase) == claim.PhaseReleased {
		c.deps.Observed.Remove(observe.BucketClaimRef(row.ID))
		return
	}
	c.deps.Observed.Upsert(observe.BucketClaimObject(*row.EnvironmentID,
		"buckets."+row.ServiceKey, row.ID, module.ClaimStatus{
			Phase:   row.Phase,
			Waiting: c.WaitingReason(row.ID),
		}))
}

func bucketWait(phase claim.Phase) string {
	switch phase {
	case claim.PhaseBound:
		return "provisioning the bucket"
	case claim.PhaseReleasing, claim.PhaseReleased:
		return "the bucket is being released"
	default:
		return "waiting for the object store"
	}
}

// bucketOwnerBase is the human part of generated bucket identities.
func bucketOwnerBase(row store.BucketClaim) string {
	if row.OwnerKind == dbstore.OwnerSystem {
		return row.SystemKey
	}
	return row.ServiceKey
}

// bucketClaimLabels stamps substrate objects with the claim's identity.
func bucketClaimLabels(row store.BucketClaim) map[string]string {
	labels := map[string]string{kubernetes.LabelClaim: row.ID.String()}
	if row.OwnerKind == dbstore.OwnerService && row.EnvironmentID != nil {
		labels[kubernetes.LabelEnvironment] = row.EnvironmentID.String()
		labels[kubernetes.LabelService] = "buckets." + row.ServiceKey
	}
	return labels
}

// dnsName reduces an owner key to a safe DNS/S3 name fragment.
func dnsName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-', r == '_', r == '/', r == '.':
			b.WriteByte('-')
		}
	}
	result := strings.Trim(b.String(), "-")
	if result == "" {
		result = "bucket"
	}
	if len(result) > 24 {
		result = strings.Trim(result[:24], "-")
	}
	return result
}
