package substrate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Hinkolas/skali/internal/claim"
	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/observe"
	"github.com/Hinkolas/skali/internal/project"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/substrate/seaweed"
	"github.com/Hinkolas/skali/internal/testdb"
)

func credentialSecret(current, previous *seaweed.Credential, retireAt string) *corev1.Secret {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "s3cred-x", Namespace: Namespace},
		Data: map[string][]byte{
			credentialAccessKey: []byte(current.AccessKey),
			credentialSecretKey: []byte(current.SecretKey),
		},
	}
	if previous != nil {
		secret.Data[credentialPreviousAccessKey] = []byte(previous.AccessKey)
		secret.Data[credentialPreviousSecretKey] = []byte(previous.SecretKey)
	}
	if retireAt != "" {
		secret.Annotations = map[string]string{AnnotationCredentialRetireAt: retireAt}
	}
	return secret
}

// desiredCredentials reads the Secret alone: the current pair always, the
// previous pair only while its retire instant lies ahead; a previous pair
// without a readable instant is already expired.
func TestDesiredCredentials(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	current := seaweed.Credential{AccessKey: "AKNEW", SecretKey: "SKNEW"}
	previous := seaweed.Credential{AccessKey: "AKOLD", SecretKey: "SKOLD"}
	cases := []struct {
		name     string
		secret   *corev1.Secret
		expected []seaweed.Credential
	}{
		{"no previous", credentialSecret(&current, nil, ""), []seaweed.Credential{current}},
		{"window open", credentialSecret(&current, &previous, now.Add(time.Hour).Format(time.RFC3339)),
			[]seaweed.Credential{current, previous}},
		{"window passed", credentialSecret(&current, &previous, now.Add(-time.Second).Format(time.RFC3339)),
			[]seaweed.Credential{current}},
		{"no instant", credentialSecret(&current, &previous, ""), []seaweed.Credential{current}},
		{"malformed instant", credentialSecret(&current, &previous, "tomorrow"), []seaweed.Credential{current}},
		{"half a previous pair", func() *corev1.Secret {
			s := credentialSecret(&current, nil, now.Add(time.Hour).Format(time.RFC3339))
			s.Data[credentialPreviousAccessKey] = []byte("AKOLD")
			return s
		}(), []seaweed.Credential{current}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.expected, desiredCredentials(tc.secret, now))
		})
	}
}

// identityDoer is the seaweed transport for the rotation tests: the
// identity print answers with the in-memory config, every other shell
// script is recorded.
type identityDoer struct {
	identities seaweed.IdentityConfig
	scripts    []string
}

func (d *identityDoer) ServiceProxyDo(context.Context, string, string, string, int, string, url.Values, []byte) ([]byte, int, error) {
	return nil, http.StatusNotFound, nil
}

func (d *identityDoer) ExecInPod(_ context.Context, _, _, _ string, command []string) (string, error) {
	script := command[len(command)-1]
	if strings.HasPrefix(script, "echo 's3.configure' |") {
		data, _ := json.Marshal(d.identities)
		return "> " + string(data) + "\n", nil
	}
	d.scripts = append(d.scripts, script)
	return "", nil
}

func (d *identityDoer) ServiceAddress(context.Context, string, string, int) (string, error) {
	return "127.0.0.1:0", nil
}

func (d *identityDoer) ForgetServiceAddress(string, string, int) {}

// rotationFixture is a provisioned bucket claim on real rows, its
// credential Secret in a fake cluster, and a controller whose identity
// writes and environment pokes are captured.
type rotationFixture struct {
	pool       *pgxpool.Pool
	db         *dbstore.Service
	fake       *fakeCluster
	doer       *identityDoer
	control    *Controller
	envID      uuid.UUID
	claim      *store.BucketClaim
	allocation *store.BucketAllocation
	poked      *[]uuid.UUID
}

func newRotationFixture(t *testing.T) *rotationFixture {
	t.Helper()
	ctx := context.Background()
	pool := testdb.New(t)
	st := store.NewStore(pool)
	dbSvc := dbstore.New(st)
	projects := project.New(st)

	suffix := uuid.Must(uuid.NewV7()).String()[24:]
	proj, err := projects.Create(ctx, "demo"+suffix, "", uuid.Nil)
	require.NoError(t, err)
	env, err := projects.CreateEnvironment(ctx, proj.ID, "production", project.EnvironmentOptions{})
	require.NoError(t, err)

	fake := &fakeCluster{}
	doer := &identityDoer{}
	client := seaweed.NewClient(doer, Namespace)
	client.SetFilerTarget("app="+seaweed.AllInOneApp, "seaweed")
	poked := []uuid.UUID{}
	controller := New(Deps{
		DB:       dbSvc,
		Cluster:  fake,
		Observed: observe.NewStore(nil),
		Seaweed:  client,
		Enqueue:  func(id uuid.UUID) { poked = append(poked, id) },
	}, Config{Managed: false})

	owner := dbstore.ServiceOwner(proj.ID, env.ID, proj.Name, "production", "files")
	claimRow, err := dbSvc.EnsureBucketClaim(ctx, owner, dbstore.BucketSpec{
		Visibility: "private", StorageQuotaBytes: 1 << 30, Versioning: "disabled",
	})
	require.NoError(t, err)
	sw, err := dbSvc.CreateObjectStore(ctx, dbstore.StoreInput{
		Name: seaweed.StoreName, Masters: 1, VolumeServers: 1,
		Replication: "000", VolumeStorageBytes: 1 << 30, Image: seaweed.Image,
	})
	require.NoError(t, err)
	allocation, err := dbSvc.RecordAllocation(ctx, dbstore.AllocationInput{
		ClaimID: claimRow.ID, StoreID: sw.ID, BucketName: "b-files-01",
		AccessKeyID: "AKOLD", CredentialSecret: "s3cred-x",
		Endpoint: InternalBucketEndpoint(), Region: seaweed.Region,
	})
	require.NoError(t, err)
	_, err = dbSvc.TransitionBucketClaim(ctx, claimRow.ID, claim.PhaseProvisioned)
	require.NoError(t, err)
	claimRow, err = dbSvc.GetBucketClaim(ctx, claimRow.ID)
	require.NoError(t, err)

	old := seaweed.Credential{AccessKey: "AKOLD", SecretKey: "SKOLD"}
	_, err = fake.ApplyAs(ctx, credentialSecret(&old, nil, ""), kube.FieldManagerPlatform, false)
	require.NoError(t, err)
	doer.identities = seaweed.IdentityConfig{Identities: []seaweed.Identity{{
		Name: "b-files-01", Credentials: []seaweed.Credential{old}, Actions: seaweed.BucketActions("b-files-01"),
	}}}
	return &rotationFixture{
		pool: pool, db: dbSvc, fake: fake, doer: doer, control: controller,
		envID: env.ID, claim: claimRow, allocation: allocation, poked: &poked,
	}
}

func (fx *rotationFixture) secret(t *testing.T) *corev1.Secret {
	t.Helper()
	secret, err := fx.fake.GetSecret(context.Background(), Namespace, "s3cred-x")
	require.NoError(t, err)
	return secret
}

func (fx *rotationFixture) liveAllocation(t *testing.T) *store.BucketAllocation {
	t.Helper()
	allocation, err := fx.db.LiveAllocation(context.Background(), fx.claim.ID)
	require.NoError(t, err)
	return allocation
}

// The commit: the Secret gains the new pair as current, the old pair as
// previous, and the retire instant; rows are untouched until the worker
// runs, and the claim is queued for it.
func TestRotateBucketCredentialsCommitsToSecret(t *testing.T) {
	t.Parallel()
	fx := newRotationFixture(t)
	ctx := context.Background()

	before := time.Now()
	result, err := fx.control.RotateBucketCredentials(ctx, fx.envID, "files", time.Hour)
	require.NoError(t, err)
	require.NotEqual(t, "AKOLD", result.AccessKey)
	require.Len(t, result.AccessKey, 20)
	require.WithinDuration(t, before.Add(time.Hour), result.RetireAt, 5*time.Second)

	secret := fx.secret(t)
	require.Equal(t, result.AccessKey, string(secret.Data[credentialAccessKey]))
	require.Len(t, secret.Data[credentialSecretKey], 40)
	require.NotEqual(t, "SKOLD", string(secret.Data[credentialSecretKey]))
	require.Equal(t, "AKOLD", string(secret.Data[credentialPreviousAccessKey]))
	require.Equal(t, "SKOLD", string(secret.Data[credentialPreviousSecretKey]))
	require.Equal(t, result.RetireAt.Format(time.RFC3339), secret.Annotations[AnnotationCredentialRetireAt])

	allocation := fx.liveAllocation(t)
	require.Equal(t, "AKOLD", allocation.AccessKeyID, "the row commits on the worker's pass")
	require.EqualValues(t, 1, allocation.CredentialVersion)
	require.Nil(t, allocation.CredentialRetireAt)
	require.Empty(t, fx.doer.scripts, "the commit touches no identity")
	require.Equal(t, 1, fx.control.queue.Len(), "the claim worker is woken")

	// Rotating again inside the window drops the older previous pair.
	again, err := fx.control.RotateBucketCredentials(ctx, fx.envID, "files", 2*time.Hour)
	require.NoError(t, err)
	secret = fx.secret(t)
	require.Equal(t, again.AccessKey, string(secret.Data[credentialAccessKey]))
	require.Equal(t, result.AccessKey, string(secret.Data[credentialPreviousAccessKey]))
}

// Preconditions: no live claim, an unprovisioned claim, and a fenced
// bucket are refused before anything is written.
func TestRotateBucketCredentialsRefusals(t *testing.T) {
	t.Parallel()
	fx := newRotationFixture(t)
	ctx := context.Background()

	_, err := fx.control.RotateBucketCredentials(ctx, fx.envID, "uploads", time.Hour)
	require.ErrorIs(t, err, dbstore.ErrNotFound)

	require.NoError(t, fx.db.FenceAllocation(ctx, fx.allocation.ID))
	_, err = fx.control.RotateBucketCredentials(ctx, fx.envID, "files", time.Hour)
	require.ErrorIs(t, err, ErrBucketFenced)
	require.NoError(t, fx.db.UnfenceAllocation(ctx, fx.allocation.ID))

	owner := dbstore.ServiceOwner(*fx.claim.ProjectID, fx.envID, "demo", "production", "uploads")
	_, err = fx.db.EnsureBucketClaim(ctx, owner, dbstore.BucketSpec{
		Visibility: "private", StorageQuotaBytes: 1 << 30, Versioning: "disabled",
	})
	require.NoError(t, err)
	_, err = fx.control.RotateBucketCredentials(ctx, fx.envID, "uploads", time.Hour)
	require.ErrorIs(t, err, ErrBucketNotProvisioned)

	secret := fx.secret(t)
	require.Equal(t, "AKOLD", string(secret.Data[credentialAccessKey]))
	require.Empty(t, secret.Data[credentialPreviousAccessKey])
}

// The worker's half, step by step: a key the row has not seen commits
// exactly once and pokes the environment; a previous pair past its instant
// retires (identity pruned, Secret cleaned, deadline cleared); a deadline
// without a previous pair is cleared; a fenced bucket cleans up without
// touching the absent identity.
func TestReconcileCredentialRotation(t *testing.T) {
	t.Parallel()
	fx := newRotationFixture(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	current := seaweed.Credential{AccessKey: "AKNEW", SecretKey: "SKNEW"}
	previous := seaweed.Credential{AccessKey: "AKOLD", SecretKey: "SKOLD"}
	retireAt := now.Add(time.Hour)

	// Secret committed with the overlap open; the row still names the old
	// key.
	secret := fx.secret(t)
	updated := credentialSecret(&current, &previous, retireAt.Format(time.RFC3339))
	updated.ResourceVersion = secret.ResourceVersion
	_, err := fx.fake.UpdateSecret(ctx, updated)
	require.NoError(t, err)

	allocation := fx.liveAllocation(t)
	require.NoError(t, fx.control.reconcileCredentialRotation(ctx, *fx.claim, allocation, fx.secret(t), now))
	require.Equal(t, "AKNEW", allocation.AccessKeyID)
	require.EqualValues(t, 2, allocation.CredentialVersion)
	require.True(t, allocation.CredentialRetireAt.Equal(retireAt))
	live := fx.liveAllocation(t)
	require.Equal(t, "AKNEW", live.AccessKeyID)
	require.EqualValues(t, 2, live.CredentialVersion)
	require.True(t, live.CredentialRetireAt.Equal(retireAt))
	require.Equal(t, []uuid.UUID{fx.envID}, *fx.poked, "the bump rolls the consumers")
	require.Empty(t, fx.doer.scripts)
	require.Equal(t, "AKOLD", string(fx.secret(t).Data[credentialPreviousAccessKey]), "the overlap stays open")

	// Repeated inside the window: nothing changes, nobody is poked.
	require.NoError(t, fx.control.reconcileCredentialRotation(ctx, *fx.claim, live, fx.secret(t), now.Add(time.Minute)))
	require.EqualValues(t, 2, fx.liveAllocation(t).CredentialVersion)
	require.Len(t, *fx.poked, 1)
	require.Empty(t, fx.doer.scripts)

	// The instant passes: the identity keeps the current pair only, the
	// Secret loses the previous pair and the instant, the row's deadline
	// clears.
	fx.doer.identities.Identities[0].Credentials = []seaweed.Credential{previous, current}
	require.NoError(t, fx.control.reconcileCredentialRotation(ctx, *fx.claim, live, fx.secret(t), retireAt))
	require.Len(t, fx.doer.scripts, 1)
	require.Contains(t, fx.doer.scripts[0], "-access_key=AKOLD -delete -apply")
	require.NotContains(t, fx.doer.scripts[0], "-access_key=AKNEW -delete")
	cleaned := fx.secret(t)
	require.Equal(t, "AKNEW", string(cleaned.Data[credentialAccessKey]))
	require.Equal(t, "SKNEW", string(cleaned.Data[credentialSecretKey]))
	require.NotContains(t, cleaned.Data, credentialPreviousAccessKey)
	require.NotContains(t, cleaned.Data, credentialPreviousSecretKey)
	require.NotContains(t, cleaned.Annotations, AnnotationCredentialRetireAt)
	require.Nil(t, live.CredentialRetireAt)
	require.Nil(t, fx.liveAllocation(t).CredentialRetireAt)
	require.Len(t, *fx.poked, 1)

	// Steady state is a read-only pass.
	fx.doer.scripts = nil
	require.NoError(t, fx.control.reconcileCredentialRotation(ctx, *fx.claim, live, fx.secret(t), retireAt.Add(time.Hour)))
	require.Empty(t, fx.doer.scripts)
	require.EqualValues(t, 2, fx.liveAllocation(t).CredentialVersion)

	// A deadline left behind without a previous pair (a retire interrupted
	// between the Secret rewrite and the row) is cleared.
	committed, err := fx.db.BeginAllocationCredentialRotation(ctx, allocation.ID, "AKNEW", retireAt)
	require.NoError(t, err)
	require.False(t, committed)
	_, err = fx.pool.Exec(ctx, "UPDATE bucket_allocations SET credential_retire_at = $2 WHERE id = $1", allocation.ID, retireAt)
	require.NoError(t, err)
	live = fx.liveAllocation(t)
	require.NotNil(t, live.CredentialRetireAt)
	require.NoError(t, fx.control.reconcileCredentialRotation(ctx, *fx.claim, live, fx.secret(t), now))
	require.Nil(t, fx.liveAllocation(t).CredentialRetireAt)

	// Fenced with an expired overlap: the identity is absent and stays
	// untouched; the Secret and the row still settle.
	stale := credentialSecret(&current, &previous, now.Add(-time.Minute).Format(time.RFC3339))
	stale.ResourceVersion = fx.secret(t).ResourceVersion
	_, err = fx.fake.UpdateSecret(ctx, stale)
	require.NoError(t, err)
	require.NoError(t, fx.db.FenceAllocation(ctx, allocation.ID))
	live = fx.liveAllocation(t)
	require.NoError(t, fx.control.reconcileCredentialRotation(ctx, *fx.claim, live, fx.secret(t), now))
	require.Empty(t, fx.doer.scripts, "a fenced bucket has no identity to prune")
	require.NotContains(t, fx.secret(t).Data, credentialPreviousAccessKey)
	require.Nil(t, fx.liveAllocation(t).CredentialRetireAt)
}

// A stale read never overwrites a concurrent write: the Secret update is
// a compare-and-swap, and the commit re-reads once.
func TestRotateBucketCredentialsRetriesConflict(t *testing.T) {
	t.Parallel()
	fx := newRotationFixture(t)
	ctx := context.Background()

	// Someone else (a retire on the worker) rewrites the Secret between
	// the commit's read and write: the fake bumps the version on every
	// stored write, so a write carrying the old version conflicts.
	secret := fx.secret(t)
	stale := secret.DeepCopy()
	bumped := secret.DeepCopy()
	bumped.Data["marker"] = []byte("x")
	_, err := fx.fake.UpdateSecret(ctx, bumped)
	require.NoError(t, err)
	_, err = fx.fake.UpdateSecret(ctx, stale)
	require.Error(t, err, "the fake refuses a stale resource version")

	result, err := fx.control.RotateBucketCredentials(ctx, fx.envID, "files", time.Hour)
	require.NoError(t, err)
	after := fx.secret(t)
	require.Equal(t, result.AccessKey, string(after.Data[credentialAccessKey]))
	require.Equal(t, "x", string(after.Data["marker"]), "the commit builds on the latest read")
}
