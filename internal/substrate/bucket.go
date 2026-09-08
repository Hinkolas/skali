package substrate

import (
	"context"
	"errors"
	"fmt"
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
	c.EnqueueObjectStore()
	ready, reason, err := c.objectStoreReady(ctx, *sw)
	if err != nil {
		return false, err
	}
	if !ready {
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
	// The published endpoint follows the installation: gaining or losing
	// the public S3 domain republishes it, and consumers roll through the
	// mirror change.
	if endpoint := c.bucketEndpoint(); allocation.Endpoint != endpoint {
		if err := c.deps.DB.SetAllocationEndpoint(ctx, allocation.ID, endpoint); err != nil {
			return false, err
		}
		allocation.Endpoint = endpoint
	}
	accessKey, secretKey, err := c.ensureBucketCredentialSecret(ctx, row, *allocation)
	if err != nil {
		return false, err
	}
	if err := c.deps.Seaweed.EnsureBucket(ctx, allocation.BucketName); err != nil {
		return false, err
	}
	if err := c.deps.Seaweed.EnsureIdentity(ctx, seaweed.Identity{
		Name:        allocation.BucketName,
		Credentials: []seaweed.Credential{{AccessKey: accessKey, SecretKey: secretKey}},
		Actions:     seaweed.BucketActions(allocation.BucketName),
	}); err != nil {
		return false, err
	}
	if err := c.ensureBucketOutputMirror(ctx, row, *allocation, accessKey, secretKey); err != nil {
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
	return c.deps.DB.RecordAllocation(ctx, dbstore.AllocationInput{
		ClaimID:          row.ID,
		StoreID:          sw.ID,
		BucketName:       "b-" + dnsName(bucketOwnerBase(row)) + "-" + utils.ShortID(row.ID),
		AccessKeyID:      seaweed.GenerateAccessKey(),
		CredentialSecret: "s3cred-" + utils.ShortID(row.ID),
		Endpoint:         c.bucketEndpoint(),
		Region:           seaweed.Region,
	})
}

// bucketEndpoint is the endpoint published to consumers: the public S3
// domain when the installation configures one (presigned URLs resolve
// publicly), the in-cluster service URL otherwise.
func (c *Controller) bucketEndpoint() string {
	if c.cfg.S3Domain != "" {
		return "https://" + c.cfg.S3Domain
	}
	return fmt.Sprintf("http://%s.%s.svc.cluster.local:%d", seaweed.S3Service, Namespace, seaweed.S3Port)
}

// ensureBucketCredentialSecret creates the claim's keypair Secret on first
// provisioning and returns the current keys. Secret keys exist only in
// Secrets; they are never logged or persisted elsewhere.
func (c *Controller) ensureBucketCredentialSecret(ctx context.Context, row store.BucketClaim, allocation store.BucketAllocation) (string, string, error) {
	existing, err := c.deps.Cluster.GetSecret(ctx, Namespace, allocation.CredentialSecret)
	if err == nil {
		return string(existing.Data["access_key"]), string(existing.Data["secret_key"]), nil
	}
	if !apierrors.IsNotFound(err) {
		return "", "", fmt.Errorf("substrate: read bucket credential secret: %w", err)
	}
	secretKey := seaweed.GenerateSecretKey()
	secret := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      allocation.CredentialSecret,
			Namespace: Namespace,
			Labels:    bucketClaimLabels(row),
		},
		Type: corev1.SecretTypeOpaque,
		StringData: map[string]string{
			"access_key": allocation.AccessKeyID,
			"secret_key": secretKey,
		},
	}
	if _, err := c.deps.Cluster.ApplyAs(ctx, secret, kube.FieldManagerPlatform, false); err != nil {
		return "", "", fmt.Errorf("substrate: apply bucket credential secret: %w", err)
	}
	return allocation.AccessKeyID, secretKey, nil
}

// ensureBucketOutputMirror writes the service claim's connection outputs
// into its environment namespace; the five keys mirror the compiler's
// bucket output catalog. System claims publish outputs through the internal
// claim API instead.
func (c *Controller) ensureBucketOutputMirror(ctx context.Context, row store.BucketClaim, allocation store.BucketAllocation, accessKey, secretKey string) error {
	if row.OwnerKind != dbstore.OwnerService {
		return nil
	}
	project, environment, service, ok := ownerNames(row.OwnerRef)
	if !ok {
		return fmt.Errorf("substrate: malformed owner ref %q", row.OwnerRef)
	}
	environmentID := ""
	if row.EnvironmentID != nil {
		environmentID = row.EnvironmentID.String()
	}
	secret := kubernetes.RenderOutputSecret(project, environment, environmentID,
		"buckets", service, map[string][]byte{
			"endpoint":   []byte(allocation.Endpoint),
			"name":       []byte(allocation.BucketName),
			"region":     []byte(allocation.Region),
			"access_key": []byte(accessKey),
			"secret_key": []byte(secretKey),
		})
	if _, err := c.deps.Cluster.ApplyAs(ctx, secret, kube.FieldManagerPlatform, false); err != nil {
		if apierrors.IsNotFound(err) {
			// The environment namespace is created by the environment
			// reconciler; until it exists the claim visibly waits.
			return errWaiting{reason: "environment namespace not created yet"}
		}
		return fmt.Errorf("substrate: apply bucket output mirror: %w", err)
	}
	return nil
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
