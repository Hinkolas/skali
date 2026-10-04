package substrate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/Hinkolas/skali/internal/claim"
	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/substrate/seaweed"
)

// A bucket's credential Secret (s3cred-<id> in skali-platform) is the one
// source of truth for the keypairs its identity accepts. The current pair
// sits under access_key/secret_key. During a rotation's overlap the pair
// being retired sits under previous_access_key/previous_secret_key and the
// annotation names the instant it stops being accepted. Rows never carry a
// secret key; the allocation row projects the deadline and counts versions.
const (
	credentialAccessKey         = "access_key"
	credentialSecretKey         = "secret_key"
	credentialPreviousAccessKey = "previous_access_key"
	credentialPreviousSecretKey = "previous_secret_key"
	// AnnotationCredentialRetireAt is the RFC 3339 instant the previous
	// keypair of a rotating bucket retires.
	AnnotationCredentialRetireAt = "skali.dev/credential-retire-at"
)

var (
	// ErrBucketNotProvisioned: the bucket claim exists but has no live,
	// provisioned bucket to rotate yet.
	ErrBucketNotProvisioned = errors.New("substrate: the bucket is not provisioned")
	// ErrBucketFenced: a restore holds the bucket; its identity is absent
	// until the fence lifts, so there is nothing to rotate against.
	ErrBucketFenced = errors.New("substrate: the bucket is fenced by a restore")
)

// BucketRotation is what one rotation committed: the new access key id
// (the row reports it once the claim worker catches up) and the instant
// the previous keypair retires.
type BucketRotation struct {
	AccessKey string
	RetireAt  time.Time
}

// RotateBucketCredentials issues a new keypair for the service's bucket by
// committing it to the credential Secret: the new pair becomes current, the
// pair it replaces becomes previous with the retire-at annotation, and the
// claim worker does everything else level-triggered from that commit (add
// the new key at the store, mirror it into the environment, bump the
// version so the consumers roll, retire the previous key once the instant
// passes). An older previous pair from a rotation still inside its window
// is dropped by the commit and retires at once. The write is a
// compare-and-swap on the Secret's resource version so it never overwrites
// a concurrent retire; a lost race is re-read once.
func (c *Controller) RotateBucketCredentials(ctx context.Context, environmentID uuid.UUID, serviceKey string, retireAfter time.Duration) (BucketRotation, error) {
	claimRow, err := c.deps.DB.LiveServiceBucketClaim(ctx, environmentID, serviceKey)
	if err != nil {
		return BucketRotation{}, err
	}
	if claim.Phase(claimRow.Phase) != claim.PhaseProvisioned {
		return BucketRotation{}, ErrBucketNotProvisioned
	}
	allocation, err := c.deps.DB.LiveAllocation(ctx, claimRow.ID)
	if err != nil {
		if errors.Is(err, dbstore.ErrNotFound) {
			return BucketRotation{}, ErrBucketNotProvisioned
		}
		return BucketRotation{}, err
	}
	if allocation.FencedAt != nil {
		return BucketRotation{}, ErrBucketFenced
	}

	next := seaweed.Credential{AccessKey: seaweed.GenerateAccessKey(), SecretKey: seaweed.GenerateSecretKey()}
	retireAt := time.Now().Add(retireAfter).UTC().Truncate(time.Second)
	for attempt := 0; ; attempt++ {
		secret, err := c.deps.Cluster.GetSecret(ctx, Namespace, allocation.CredentialSecret)
		if err != nil {
			return BucketRotation{}, fmt.Errorf("substrate: read bucket credentials: %w", err)
		}
		updated := secret.DeepCopy()
		updated.StringData = nil
		if updated.Data == nil {
			updated.Data = map[string][]byte{}
		}
		current := currentCredential(secret)
		updated.Data[credentialPreviousAccessKey] = []byte(current.AccessKey)
		updated.Data[credentialPreviousSecretKey] = []byte(current.SecretKey)
		updated.Data[credentialAccessKey] = []byte(next.AccessKey)
		updated.Data[credentialSecretKey] = []byte(next.SecretKey)
		if updated.Annotations == nil {
			updated.Annotations = map[string]string{}
		}
		updated.Annotations[AnnotationCredentialRetireAt] = retireAt.Format(time.RFC3339)
		_, err = c.deps.Cluster.UpdateSecret(ctx, updated)
		if err == nil {
			break
		}
		if apierrors.IsConflict(err) && attempt == 0 {
			continue
		}
		return BucketRotation{}, fmt.Errorf("substrate: commit rotated bucket credentials: %w", err)
	}
	slog.Info("substrate: bucket credentials rotated", "bucket", allocation.BucketName,
		"service", serviceKey, "retireAt", retireAt.Format(time.RFC3339))
	c.EnqueueBucketClaim(claimRow.ID)
	return BucketRotation{AccessKey: next.AccessKey, RetireAt: retireAt}, nil
}

// reconcileCredentialRotation is the claim worker's half of a rotation,
// run on every provisioning pass after the identity holds the desired key
// set and the mirror holds the current pair. It commits a key the row has
// not seen (the version bump that rolls the consumers; exactly once per
// key, and only now that the mirror is right), retires the previous pair
// once its instant has passed, and clears a deadline whose previous pair
// is already gone. Every branch is derived from the Secret, so a pass
// interrupted anywhere converges on the next one. wake asks the commit to
// wake the environment itself; the pass leaves that to the publication
// when the mirror changed in it.
func (c *Controller) reconcileCredentialRotation(ctx context.Context, row store.BucketClaim,
	allocation *store.BucketAllocation, secret *corev1.Secret, now time.Time, wake bool) error {
	current := currentCredential(secret)
	retireAt, hasDeadline := credentialRetireAt(secret)
	if allocation.AccessKeyID != current.AccessKey {
		deadline := now
		if hasDeadline {
			deadline = retireAt
		}
		committed, err := c.deps.DB.BeginAllocationCredentialRotation(ctx, allocation.ID, current.AccessKey, deadline)
		if err != nil {
			return err
		}
		if committed {
			allocation.AccessKeyID = current.AccessKey
			allocation.CredentialVersion++
			allocation.CredentialRetireAt = &deadline
			slog.Info("substrate: bucket credential version bumped", "bucket", allocation.BucketName,
				"version", allocation.CredentialVersion, "retireAt", deadline.Format(time.RFC3339))
			if wake && c.deps.Enqueue != nil && row.EnvironmentID != nil {
				c.deps.Enqueue(*row.EnvironmentID)
			}
		}
	}

	_, hasPrevious := previousCredential(secret)
	switch {
	case hasPrevious && (!hasDeadline || !now.Before(retireAt)):
		// A fenced bucket has no identity at all; the fence lift derives
		// the one-key set from the cleaned Secret, so only the ensure is
		// skipped here.
		if allocation.FencedAt == nil {
			if err := c.deps.Seaweed.EnsureIdentity(ctx, seaweed.Identity{
				Name:        allocation.BucketName,
				Credentials: []seaweed.Credential{current},
				Actions:     seaweed.BucketActions(allocation.BucketName),
			}); err != nil {
				return fmt.Errorf("substrate: retire previous credentials of %s: %w", allocation.BucketName, err)
			}
		}
		updated := secret.DeepCopy()
		updated.StringData = nil
		delete(updated.Data, credentialPreviousAccessKey)
		delete(updated.Data, credentialPreviousSecretKey)
		delete(updated.Annotations, AnnotationCredentialRetireAt)
		if _, err := c.deps.Cluster.UpdateSecret(ctx, updated); err != nil {
			return fmt.Errorf("substrate: clear retired credentials of %s: %w", allocation.BucketName, err)
		}
		if err := c.deps.DB.FinishAllocationCredentialRotation(ctx, allocation.ID); err != nil {
			return err
		}
		allocation.CredentialRetireAt = nil
		slog.Info("substrate: previous bucket credentials retired", "bucket", allocation.BucketName)
		c.pokeProbe()
	case !hasPrevious && allocation.CredentialRetireAt != nil:
		if err := c.deps.DB.FinishAllocationCredentialRotation(ctx, allocation.ID); err != nil {
			return err
		}
		allocation.CredentialRetireAt = nil
	}
	return nil
}

// desiredCredentials derives the keypairs a bucket identity must accept
// from its credential Secret alone: the current pair, plus the previous
// pair while its retire instant lies ahead. A previous pair without a
// readable instant counts as expired, the safe side. Every identity ensure
// (provisioning, fence lift) goes through here so the overlap survives
// drift repair and restarts and no caller prunes a key another one added.
func desiredCredentials(secret *corev1.Secret, now time.Time) []seaweed.Credential {
	credentials := []seaweed.Credential{currentCredential(secret)}
	if previous, ok := previousCredential(secret); ok {
		if retireAt, ok := credentialRetireAt(secret); ok && now.Before(retireAt) {
			credentials = append(credentials, previous)
		}
	}
	return credentials
}

func currentCredential(secret *corev1.Secret) seaweed.Credential {
	return seaweed.Credential{
		AccessKey: string(secret.Data[credentialAccessKey]),
		SecretKey: string(secret.Data[credentialSecretKey]),
	}
}

func previousCredential(secret *corev1.Secret) (seaweed.Credential, bool) {
	previous := seaweed.Credential{
		AccessKey: string(secret.Data[credentialPreviousAccessKey]),
		SecretKey: string(secret.Data[credentialPreviousSecretKey]),
	}
	return previous, previous.AccessKey != "" && previous.SecretKey != ""
}

func credentialRetireAt(secret *corev1.Secret) (time.Time, bool) {
	value, ok := secret.Annotations[AnnotationCredentialRetireAt]
	if !ok {
		return time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, false
	}
	return at, true
}
