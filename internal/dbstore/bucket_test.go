package dbstore

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/claim"
	"github.com/Hinkolas/skali/internal/lifecycle"
	"github.com/Hinkolas/skali/internal/store"
)

func TestStoreStateMachineInvariants(t *testing.T) {
	t.Parallel()
	require.NoError(t, lifecycle.Verify(StoreStates))
	require.Equal(t, StateActive, StoreStates.Initial())
	require.True(t, StoreStates.Terminal(StateReleased))
}

func bucketSpec() BucketSpec {
	return BucketSpec{
		Visibility:        "private",
		StorageQuotaBytes: 1 << 30,
		Versioning:        "disabled",
	}
}

func (f *fixture) objectStore(t *testing.T) *store.ObjectStore {
	t.Helper()
	row, err := f.svc.CreateObjectStore(context.Background(), StoreInput{
		Name:               "seaweed",
		Masters:            1,
		VolumeServers:      1,
		Replication:        "000",
		VolumeStorageBytes: 1 << 30,
		Image:              "seaweedfs:test",
	})
	require.NoError(t, err)
	return row
}

func (f *fixture) allocate(t *testing.T, claimID, storeID uuid.UUID) *store.BucketAllocation {
	t.Helper()
	allocation, err := f.svc.RecordAllocation(context.Background(), AllocationInput{
		ClaimID:          claimID,
		StoreID:          storeID,
		BucketName:       "b-files-01",
		AccessKeyID:      "AKSKALITEST01",
		CredentialSecret: "s3cred-01",
		Endpoint:         "http://seaweed-s3.skali-platform.svc.cluster.local:8333",
		Region:           "us-east-1",
	})
	require.NoError(t, err)
	return allocation
}

func TestBucketClaimPhaseWalk(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	created, err := f.svc.EnsureBucketClaim(ctx, f.owner("files"), bucketSpec())
	require.NoError(t, err)
	require.Equal(t, string(claim.PhasePending), created.Phase)
	require.Equal(t, "project/demo/environment/production/service/files", created.OwnerRef)

	sw := f.objectStore(t)
	allocation := f.allocate(t, created.ID, sw.ID)
	require.Equal(t, sw.ID, allocation.StoreID)
	bound, err := f.svc.GetBucketClaim(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, string(claim.PhaseBound), bound.Phase, "allocation is the placement and binds the claim")

	provisioned, err := f.svc.TransitionBucketClaim(ctx, created.ID, claim.PhaseProvisioned)
	require.NoError(t, err)
	require.Equal(t, string(claim.PhaseProvisioned), provisioned.Phase)

	releasing, err := f.svc.ReleaseBucketClaim(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, string(claim.PhaseReleasing), releasing.Phase)
	// Releasing is one-way: a repeat release is a no-op, not an error.
	again, err := f.svc.ReleaseBucketClaim(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, string(claim.PhaseReleasing), again.Phase)

	require.NoError(t, f.svc.CompleteBucketClaimRelease(ctx, created.ID))
	released, err := f.svc.GetBucketClaim(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, string(claim.PhaseReleased), released.Phase)
	_, err = f.svc.LiveAllocation(ctx, created.ID)
	require.ErrorIs(t, err, ErrNotFound)
	count, err := f.svc.CountStoreAllocations(ctx, sw.ID)
	require.NoError(t, err)
	require.Zero(t, count)

	// The released claim frees the owner slot and the bucket name: ensuring
	// again creates a new identity instead of resurrecting the old one.
	next, err := f.svc.EnsureBucketClaim(ctx, f.owner("files"), bucketSpec())
	require.NoError(t, err)
	require.NotEqual(t, created.ID, next.ID)
	require.Equal(t, string(claim.PhasePending), next.Phase)
}

func TestEnsureBucketClaimIdempotentAndDrift(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	first, err := f.svc.EnsureBucketClaim(ctx, f.owner("files"), bucketSpec())
	require.NoError(t, err)
	same, err := f.svc.EnsureBucketClaim(ctx, f.owner("files"), bucketSpec())
	require.NoError(t, err)
	require.Equal(t, first.ID, same.ID)
	require.Equal(t, first.UpdatedAt, same.UpdatedAt)

	// Mutable drift folds into the live claim.
	drifted := bucketSpec()
	drifted.StorageQuotaBytes = 2 << 30
	drifted.AbortUploadsAfterSeconds = 86400
	updated, err := f.svc.EnsureBucketClaim(ctx, f.owner("files"), drifted)
	require.NoError(t, err)
	require.Equal(t, first.ID, updated.ID)
	require.Equal(t, int64(2<<30), updated.StorageQuotaBytes)
	require.Equal(t, int64(86400), updated.AbortUploadsAfterSeconds)

	// The externally observable contract is immutable on a live claim.
	conflicting := bucketSpec()
	conflicting.Visibility = "public-read"
	_, err = f.svc.EnsureBucketClaim(ctx, f.owner("files"), conflicting)
	require.ErrorIs(t, err, ErrSpecConflict)
	conflicting = bucketSpec()
	conflicting.Versioning = "enabled"
	_, err = f.svc.EnsureBucketClaim(ctx, f.owner("files"), conflicting)
	require.ErrorIs(t, err, ErrSpecConflict)
}

func TestReleaseUnallocatedBucketClaimReleasesDirectly(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	created, err := f.svc.EnsureBucketClaim(ctx, f.owner("files"), bucketSpec())
	require.NoError(t, err)
	released, err := f.svc.ReleaseBucketClaim(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, string(claim.PhaseReleased), released.Phase)

	// A released claim cannot be allocated.
	sw := f.objectStore(t)
	_, err = f.svc.RecordAllocation(ctx, AllocationInput{
		ClaimID: created.ID, StoreID: sw.ID, BucketName: "b-late-01",
		AccessKeyID: "AKLATE", CredentialSecret: "s3cred-late",
		Endpoint: "http://e", Region: "us-east-1",
	})
	require.ErrorIs(t, err, ErrInvalidTransition)
}

func TestBucketAllocationIdentityImmutable(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	sw := f.objectStore(t)
	created, err := f.svc.EnsureBucketClaim(ctx, f.owner("files"), bucketSpec())
	require.NoError(t, err)

	allocation := f.allocate(t, created.ID, sw.ID)
	repeat, err := f.svc.RecordAllocation(ctx, AllocationInput{
		ClaimID: created.ID, StoreID: sw.ID, BucketName: "b-files-other",
		AccessKeyID: "AKOTHER", CredentialSecret: "s3cred-other",
		Endpoint: "http://other", Region: "us-east-1",
	})
	require.NoError(t, err)
	require.Equal(t, allocation.ID, repeat.ID)
	require.Equal(t, "b-files-01", repeat.BucketName, "generated identity never changes once recorded")
	require.EqualValues(t, 1, allocation.CredentialVersion)

	// A second claim cannot take a live bucket name.
	other, err := f.svc.EnsureBucketClaim(ctx, f.owner("uploads"), bucketSpec())
	require.NoError(t, err)
	_, err = f.svc.RecordAllocation(ctx, AllocationInput{
		ClaimID: other.ID, StoreID: sw.ID, BucketName: "b-files-01",
		AccessKeyID: "AKDUP", CredentialSecret: "s3cred-dup",
		Endpoint: "http://e", Region: "us-east-1",
	})
	require.Error(t, err)

	require.NoError(t, f.svc.BumpAllocationCredentialVersion(ctx, allocation.ID))
	require.NoError(t, f.svc.SetAllocationEndpoint(ctx, allocation.ID, "https://s3.example.test"))
	live, err := f.svc.LiveAllocation(ctx, created.ID)
	require.NoError(t, err)
	require.EqualValues(t, 2, live.CredentialVersion)
	require.Equal(t, "https://s3.example.test", live.Endpoint)
	require.Equal(t, "s3cred-01", live.CredentialSecret)

	allocations, err := f.svc.ListStoreAllocations(ctx, sw.ID)
	require.NoError(t, err)
	require.Len(t, allocations, 1)
}

func TestObjectStoreLifecycle(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	sw := f.objectStore(t)
	found, err := f.svc.LiveObjectStore(ctx)
	require.NoError(t, err)
	require.Equal(t, sw.ID, found.ID)

	// One live store per installation.
	_, err = f.svc.CreateObjectStore(ctx, StoreInput{
		Name: "seaweed", Masters: 1, VolumeServers: 1,
		Replication: "000", VolumeStorageBytes: 1, Image: "seaweedfs:test",
	})
	require.Error(t, err)

	stopped, err := f.svc.TransitionObjectStore(ctx, sw.ID, StateStopped)
	require.NoError(t, err)
	require.Equal(t, StateStopped, stopped.State)
	_, err = f.svc.TransitionObjectStore(ctx, sw.ID, StateReleased)
	require.ErrorIs(t, err, ErrInvalidTransition)
	_, err = f.svc.TransitionObjectStore(ctx, sw.ID, StateActive)
	require.NoError(t, err)
	_, err = f.svc.TransitionObjectStore(ctx, sw.ID, StateReleasing)
	require.NoError(t, err)
	_, err = f.svc.TransitionObjectStore(ctx, sw.ID, StateReleased)
	require.NoError(t, err)

	// A released store frees the name.
	_, err = f.svc.LiveObjectStore(ctx)
	require.ErrorIs(t, err, ErrNotFound)
	replacement := f.objectStore(t)
	require.NotEqual(t, sw.ID, replacement.ID)
}

func TestActiveBucketClaimCountFollowsEnvironmentState(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	sw := f.objectStore(t)
	created, err := f.svc.EnsureBucketClaim(ctx, f.owner("files"), bucketSpec())
	require.NoError(t, err)

	// A pending claim already counts: the first claim must be able to bring
	// the store up before any allocation exists.
	count, err := f.svc.ActiveBucketClaimCount(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, count)
	f.allocate(t, created.ID, sw.ID)

	// Taking the environment down removes its claims from the active count:
	// the local-dev store may stop.
	rows, err := f.st.MarkEnvironmentDown(ctx, f.environmentID)
	require.NoError(t, err)
	require.EqualValues(t, 1, rows)
	count, err = f.svc.ActiveBucketClaimCount(ctx)
	require.NoError(t, err)
	require.Zero(t, count)

	// A releasing claim counts regardless of environment state: teardown
	// needs the store running.
	_, err = f.svc.ReleaseBucketClaim(ctx, created.ID)
	require.NoError(t, err)
	count, err = f.svc.ActiveBucketClaimCount(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, count)
	require.NoError(t, f.svc.CompleteBucketClaimRelease(ctx, created.ID))
	count, err = f.svc.ActiveBucketClaimCount(ctx)
	require.NoError(t, err)
	require.Zero(t, count)
}

func TestSystemDatabaseClaimIgnoredWhileStoreStopped(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	// The seaweed metadata claim keeps the dev pool awake while the store
	// runs, and releases its hold while the store is stopped (the quiet dev
	// platform, owner decision 2026-07-29).
	pool := f.cluster(t, "pg17-shared")
	metadata, err := f.svc.EnsureClaim(ctx, SystemOwner("object-storage/metadata"), spec())
	require.NoError(t, err)
	_, err = f.svc.BindClaim(ctx, metadata.ID, pool.ID)
	require.NoError(t, err)

	count, err := f.svc.ActiveClaimCount(ctx, pool.ID)
	require.NoError(t, err)
	require.EqualValues(t, 1, count, "a running store holds the pool awake")

	sw := f.objectStore(t)
	_, err = f.svc.TransitionObjectStore(ctx, sw.ID, StateStopped)
	require.NoError(t, err)
	count, err = f.svc.ActiveClaimCount(ctx, pool.ID)
	require.NoError(t, err)
	require.Zero(t, count, "a stopped store releases its hold on the pool")

	// Other system claims are unaffected by the store state.
	other, err := f.svc.EnsureClaim(ctx, SystemOwner("synthetic/other"), spec())
	require.NoError(t, err)
	_, err = f.svc.BindClaim(ctx, other.ID, pool.ID)
	require.NoError(t, err)
	count, err = f.svc.ActiveClaimCount(ctx, pool.ID)
	require.NoError(t, err)
	require.EqualValues(t, 1, count)

	// Resuming the store restores the metadata claim's hold.
	_, err = f.svc.TransitionObjectStore(ctx, sw.ID, StateActive)
	require.NoError(t, err)
	count, err = f.svc.ActiveClaimCount(ctx, pool.ID)
	require.NoError(t, err)
	require.EqualValues(t, 2, count)
}
