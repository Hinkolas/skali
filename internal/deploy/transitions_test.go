package deploy

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/artifactstore"
	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/layout"
	"github.com/Hinkolas/skali/internal/manifest"
)

func databaseManifest(version, isolation, size string) string {
	return fmt.Sprintf(`name: demo
databases:
  main:
    engine: postgres
    version: %s
    isolation: %s
    storage:
      size: %s
`, version, isolation, size)
}

func bucketManifest(quota string) string {
	return fmt.Sprintf(`name: demo
buckets:
  media:
    quotas:
      storage: %s
`, quota)
}

// compiledQuota is the storage quota a bucket manifest compiles to.
func compiledQuota(t *testing.T, quota string) int64 {
	t.Helper()
	document, err := manifest.Parse([]byte(bucketManifest(quota)), "skali.yml")
	require.NoError(t, err)
	result, err := compiler.Compile(document)
	require.NoError(t, err)
	return result.Definition.Buckets["media"].StorageQuotaBytes
}

// liveDatabase records a live claim for databases.main the way the
// reconciler does once a revision declaring it rolls out.
func (f *fixture) liveDatabase(t *testing.T, major int, isolation string) {
	t.Helper()
	_, err := dbstore.New(f.st).EnsureClaim(context.Background(),
		dbstore.ServiceOwner(f.projectID, f.environmentID, "demo", "production", "main"),
		dbstore.ClaimSpec{Engine: "postgres", Major: major, Isolation: isolation, Availability: "single"})
	require.NoError(t, err)
}

// liveBucket records a live claim for buckets.media with a storage quota.
func (f *fixture) liveBucket(t *testing.T, quota int64) {
	t.Helper()
	_, err := dbstore.New(f.st).EnsureBucketClaim(context.Background(),
		dbstore.ServiceOwner(f.projectID, f.environmentID, "demo", "production", "media"),
		dbstore.BucketSpec{Visibility: "private", Versioning: "disabled", StorageQuotaBytes: quota})
	require.NoError(t, err)
}

// open opens a deployment of definitionVersion with destructive changes
// allowed, so only the transition checks can refuse it.
func (f *fixture) open(t *testing.T, jsvc *journal.Service, definitionVersion uuid.UUID) error {
	t.Helper()
	_, err := f.deploy.Open(context.Background(), OpenInput{
		PlanInput: PlanInput{EnvironmentID: f.environmentID, DefinitionVersionID: definitionVersion},
		Actor:     "test", AllowDestructive: true, RegistryConfigured: true, Journal: jsvc,
		Capabilities: []string{layout.CapabilityApplication, layout.CapabilityDatabase, layout.CapabilityObjectStorage},
	})
	return err
}

// untouched asserts a refusal left nothing behind: no target, no
// deployment, no run.
func (f *fixture) untouched(t *testing.T, target *uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	current, err := f.deploy.Target(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, target, current.TargetRevisionID)
	_, err = f.st.GetPreparingDeploymentForEnvironment(ctx, f.environmentID)
	require.Error(t, err, "a refused change opens no deployment")
	_, err = f.st.GetRunningRunByEnvironment(ctx, &f.environmentID)
	require.Error(t, err, "a refused change leaves no run")
}

// A live database's engine, version, and isolation cannot change: the
// plan preview and the open refuse naming the field and both values,
// confirming destructive changes does not get past it, and nothing is
// opened. Changes the claim folds in place, and new databases, pass.
func TestTransitionsRefuseMovingALiveDatabase(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	jsvc := journal.NewService(f.st, "test")
	f.liveDatabase(t, 17, "shared")

	refusals := []struct {
		manifest, field, from, to string
	}{
		{databaseManifest("18", "shared", "1GB"), "version", "17", "18"},
		{databaseManifest("17", "dedicated", "1GB"), "isolation", "shared", "dedicated"},
	}
	for index, refusal := range refusals {
		version := f.submit(t, refusal.manifest, int64(index))
		_, err := f.deploy.PlanPreview(ctx, PlanInput{EnvironmentID: f.environmentID, DefinitionVersionID: version})
		var transition *TransitionError
		require.ErrorAs(t, err, &transition)
		require.Equal(t, TransitionError{Service: "databases.main", Field: refusal.field, From: refusal.from, To: refusal.to},
			*transition)
		require.ErrorAs(t, f.open(t, jsvc, version), &transition)
		f.untouched(t, nil)
	}
	require.Contains(t, (&TransitionError{Service: "databases.main", Field: "version", From: "17", To: "18"}).Error(),
		"Keep version 17, or declare a new database with version 18")

	// Storage folds into the live claim in place.
	resized := f.submit(t, databaseManifest("17", "shared", "2GB"), 2)
	_, err := f.deploy.PlanPreview(ctx, PlanInput{EnvironmentID: f.environmentID, DefinitionVersionID: resized})
	require.NoError(t, err)

	// A database without a live claim is created as declared.
	created := f.submit(t, `name: demo
databases:
  main:
    engine: postgres
    version: 17
  reports:
    engine: postgres
    version: 18
`, 3)
	_, err = f.deploy.PlanPreview(ctx, PlanInput{EnvironmentID: f.environmentID, DefinitionVersionID: created})
	require.NoError(t, err)
}

// The live claim decides, not the active revision: after a rollout that
// asked for 18 never applied it, deploying 18 again is refused although
// the plan against the target shows no change, and 17 is accepted.
func TestTransitionsFollowLiveStateNotTheTarget(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	resolver := &artifactstore.Fake{Store: f.artifacts, ProjectID: f.projectID}
	upgrade := f.submit(t, databaseManifest("18", "shared", "1GB"), 0)
	prepared, err := f.deploy.Prepare(ctx, PrepareInput{
		EnvironmentID: f.environmentID, DefinitionVersionID: upgrade, Resolver: resolver,
	})
	require.NoError(t, err)
	require.NoError(t, f.deploy.Promote(ctx, prepared))
	f.liveDatabase(t, 17, "shared")

	_, err = f.deploy.PlanPreview(ctx, PlanInput{EnvironmentID: f.environmentID, DefinitionVersionID: upgrade})
	var transition *TransitionError
	require.ErrorAs(t, err, &transition)
	require.Equal(t, "17", transition.From)

	current := f.submit(t, databaseManifest("17", "shared", "1GB"), 1)
	_, err = f.deploy.PlanPreview(ctx, PlanInput{EnvironmentID: f.environmentID, DefinitionVersionID: current})
	require.NoError(t, err)
}

// Promotion checks again: a claim that went live after the open refuses
// the prepared revision, and the target stays where it was.
func TestTransitionsRecheckedAtPromotion(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	resolver := &artifactstore.Fake{Store: f.artifacts, ProjectID: f.projectID}
	version := f.submit(t, databaseManifest("18", "shared", "1GB"), 0)
	prepared, err := f.deploy.Prepare(ctx, PrepareInput{
		EnvironmentID: f.environmentID, DefinitionVersionID: version, Resolver: resolver,
	})
	require.NoError(t, err)
	f.liveDatabase(t, 17, "shared")

	var transition *TransitionError
	require.ErrorAs(t, f.deploy.Promote(ctx, prepared), &transition)
	target, err := f.deploy.Target(ctx, f.environmentID)
	require.NoError(t, err)
	require.Nil(t, target.TargetRevisionID)
}

// A rollback is held to the same rule: an older revision naming another
// version than the live database's is refused before any run begins.
func TestTransitionsRefuseRollback(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	jsvc := journal.NewService(f.st, "test")
	resolver := &artifactstore.Fake{Store: f.artifacts, ProjectID: f.projectID}
	promote := func(manifest string, expected int64) uuid.UUID {
		version := f.submit(t, manifest, expected)
		prepared, err := f.deploy.Prepare(ctx, PrepareInput{
			EnvironmentID: f.environmentID, DefinitionVersionID: version, Resolver: resolver,
		})
		require.NoError(t, err)
		require.NoError(t, f.deploy.Promote(ctx, prepared))
		return prepared.RevisionID
	}
	older := promote(databaseManifest("18", "shared", "1GB"), 0)
	f.liveDatabase(t, 17, "shared")
	current := promote(databaseManifest("17", "shared", "1GB"), 1)

	_, err := f.deploy.Rollback(ctx, RollbackInput{
		EnvironmentID: f.environmentID, RevisionID: older, Actor: "test", Journal: jsvc,
	})
	var transition *TransitionError
	require.ErrorAs(t, err, &transition)
	f.untouched(t, &current)
}

// fakeUsage reports a fixed usage, or an error, and counts measurements.
type fakeUsage struct {
	mu    sync.Mutex
	used  int64
	err   error
	calls int
}

func (u *fakeUsage) BucketUsedBytes(context.Context, uuid.UUID) (int64, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.calls++
	return u.used, u.err
}

func (u *fakeUsage) set(used int64, err error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.used, u.err, u.calls = used, err, 0
}

// A quota reduction is measured against the bucket's usage now and
// refused when the bucket would sit at or over it: equality already
// refuses uploads. Increases and unchanged quotas measure nothing.
func TestTransitionsRefuseQuotaBelowUsage(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	jsvc := journal.NewService(f.st, "test")
	usage := &fakeUsage{}
	f.deploy.SetBucketUsage(usage)
	f.liveBucket(t, compiledQuota(t, "10GB"))
	preview := func(quota string, expected int64) error {
		version := f.submit(t, bucketManifest(quota), expected)
		_, err := f.deploy.PlanPreview(ctx, PlanInput{EnvironmentID: f.environmentID, DefinitionVersionID: version})
		return err
	}

	usage.set(compiledQuota(t, "8GB"), nil)
	err := preview("5GB", 0)
	var below *BucketQuotaError
	require.ErrorAs(t, err, &below)
	require.Equal(t, BucketQuotaError{Bucket: "buckets.media", UsedBytes: compiledQuota(t, "8GB"),
		QuotaBytes: compiledQuota(t, "5GB")}, *below)
	require.Contains(t, err.Error(), "Delete objects until it stores less than")
	require.ErrorAs(t, f.open(t, jsvc, f.submit(t, bucketManifest("5GB"), 1)), &below)
	f.untouched(t, nil)

	require.ErrorAs(t, preview("8GB", 2), &below, "a quota equal to the usage already refuses uploads")
	require.NoError(t, preview("9GB", 3), "a reduction the usage fits under is accepted")

	usage.set(compiledQuota(t, "8GB"), nil)
	require.NoError(t, preview("20GB", 4))
	require.NoError(t, preview("10GB", 5))
	require.Zero(t, usage.calls, "only a reduction is measured")
}

// A reduction that cannot be measured is refused, never guessed, and an
// unlimited quota becoming limited counts as a reduction.
func TestTransitionsRefuseUnmeasuredQuotaReduction(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.liveBucket(t, 0)
	version := f.submit(t, bucketManifest("5GB"), 0)
	preview := func() error {
		_, err := f.deploy.PlanPreview(ctx, PlanInput{EnvironmentID: f.environmentID, DefinitionVersionID: version})
		return err
	}

	var unavailable *BucketUsageUnavailableError
	require.ErrorAs(t, preview(), &unavailable, "a daemon without object storage cannot measure")

	usage := &fakeUsage{}
	f.deploy.SetBucketUsage(usage)
	measureFailed := errors.New("volume status: status 503")
	usage.set(0, measureFailed)
	err := preview()
	require.ErrorAs(t, err, &unavailable)
	require.ErrorIs(t, err, measureFailed)

	usage.set(compiledQuota(t, "1GB"), nil)
	require.NoError(t, preview())
	require.Equal(t, 1, usage.calls)
}

// Usage keeps changing between preview and promotion; promotion measures
// again and refuses a quota the bucket has since outgrown.
func TestTransitionsRemeasureQuotaAtPromotion(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	usage := &fakeUsage{}
	f.deploy.SetBucketUsage(usage)
	f.liveBucket(t, compiledQuota(t, "10GB"))
	version := f.submit(t, bucketManifest("5GB"), 0)

	usage.set(compiledQuota(t, "4GB"), nil)
	_, err := f.deploy.PlanPreview(ctx, PlanInput{EnvironmentID: f.environmentID, DefinitionVersionID: version})
	require.NoError(t, err)
	prepared, err := f.deploy.Prepare(ctx, PrepareInput{
		EnvironmentID: f.environmentID, DefinitionVersionID: version,
		Resolver: &artifactstore.Fake{Store: f.artifacts, ProjectID: f.projectID},
	})
	require.NoError(t, err)

	usage.set(compiledQuota(t, "6GB"), nil)
	var below *BucketQuotaError
	require.ErrorAs(t, f.deploy.Promote(ctx, prepared), &below)
	target, err := f.deploy.Target(ctx, f.environmentID)
	require.NoError(t, err)
	require.Nil(t, target.TargetRevisionID)
	claim, err := dbstore.New(f.st).LiveServiceBucketClaim(ctx, f.environmentID, "media")
	require.NoError(t, err)
	require.Equal(t, compiledQuota(t, "10GB"), claim.StorageQuotaBytes, "the live quota is unchanged")
}
