package substrate

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	"github.com/Hinkolas/skali/internal/claim"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/substrate/cnpg"
)

// provisioned brings the fixture's claim to provisioned on a ready fake
// pool and returns its tenant, which logs in as the owner (version 1).
func (fx *settleFixture) provisioned(t *testing.T) *store.DatabaseTenant {
	t.Helper()
	fx.fake.set(clusterHealthyPhase, 1, true)
	_, phase := fx.pass(t)
	require.Equal(t, claim.PhaseProvisioned, phase)
	return fx.tenant(t)
}

func (fx *settleFixture) tenant(t *testing.T) *store.DatabaseTenant {
	t.Helper()
	tenant, err := fx.db.LiveTenant(context.Background(), fx.claim.ID)
	require.NoError(t, err)
	return tenant
}

func (fx *settleFixture) secret(t *testing.T, name string) *corev1.Secret {
	t.Helper()
	secret, err := fx.fake.GetSecret(context.Background(), Namespace, name)
	require.NoError(t, err, "secret %s", name)
	return secret
}

func (fx *settleFixture) mirror(t *testing.T) *corev1.Secret {
	t.Helper()
	secret, err := fx.fake.GetSecret(context.Background(),
		kubernetes.NamespaceName(fx.envID.String()), kubernetes.OutputSecretName("databases", "data"))
	require.NoError(t, err)
	return secret
}

func TestTenantRoles(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	past := now.Add(-time.Minute)
	str := func(s string) *string { return &s }

	unconverted := store.DatabaseTenant{RoleName: "u_data", LoginRole: "u_data", CredentialSecret: "dbcred-x"}
	require.Equal(t, []cnpg.Role{{Name: "u_data", SecretName: "dbcred-x", Login: true}}, tenantRoles(unconverted, now),
		"a tenant before its first rotation renders exactly as before")

	pending := unconverted
	pending.PendingLoginRole, pending.PendingCredentialSecret = str("u_data_v2"), str("dbcred-x-v2")
	require.Equal(t, []cnpg.Role{
		{Name: "u_data", SecretName: "dbcred-x", Login: true},
		{Name: "u_data_v2", SecretName: "dbcred-x-v2", Login: true, InRoles: []string{"u_data"}},
	}, tenantRoles(pending, now), "a pending role is created beside the owner, which still logs in")

	firstWindow := store.DatabaseTenant{RoleName: "u_data", LoginRole: "u_data_v2", CredentialSecret: "dbcred-x-v2",
		PreviousLoginRole: str("u_data"), PreviousCredentialSecret: str("dbcred-x"), CredentialRetireAt: &future}
	require.Equal(t, []cnpg.Role{
		{Name: "u_data", SecretName: "dbcred-x", Login: true},
		{Name: "u_data_v2", SecretName: "dbcred-x-v2", Login: true, InRoles: []string{"u_data"}},
	}, tenantRoles(firstWindow, now), "the owner keeps its login while it is the previous role inside the window")

	firstExpired := firstWindow
	firstExpired.CredentialRetireAt = &past
	require.Equal(t, []cnpg.Role{
		{Name: "u_data", DisablePassword: true},
		{Name: "u_data_v2", SecretName: "dbcred-x-v2", Login: true, InRoles: []string{"u_data"}},
	}, tenantRoles(firstExpired, now), "past the window the owner's login and password go")

	converted := store.DatabaseTenant{RoleName: "u_data", LoginRole: "u_data_v2", CredentialSecret: "dbcred-x-v2"}
	require.Equal(t, []cnpg.Role{
		{Name: "u_data", DisablePassword: true},
		{Name: "u_data_v2", SecretName: "dbcred-x-v2", Login: true, InRoles: []string{"u_data"}},
	}, tenantRoles(converted, now))

	secondWindow := store.DatabaseTenant{RoleName: "u_data", LoginRole: "u_data_v3", CredentialSecret: "dbcred-x-v3",
		PreviousLoginRole: str("u_data_v2"), PreviousCredentialSecret: str("dbcred-x-v2"), CredentialRetireAt: &future}
	require.Equal(t, []cnpg.Role{
		{Name: "u_data", DisablePassword: true},
		{Name: "u_data_v3", SecretName: "dbcred-x-v3", Login: true, InRoles: []string{"u_data"}},
		{Name: "u_data_v2", SecretName: "dbcred-x-v2", Login: true, InRoles: []string{"u_data"}},
	}, tenantRoles(secondWindow, now))
	secondExpired := secondWindow
	secondExpired.CredentialRetireAt = &past
	require.Len(t, tenantRoles(secondExpired, now), 2, "an expired login role leaves the spec before it is dropped")
}

func TestRotationWakeup(t *testing.T) {
	t.Parallel()
	now := time.Now()
	str := func(s string) *string { return &s }
	require.Zero(t, rotationWakeup(store.DatabaseTenant{}, now), "nothing scheduled, nothing to wake for")
	soon := now.Add(30 * time.Second)
	require.Equal(t, 30*time.Second, rotationWakeup(store.DatabaseTenant{PreviousLoginRole: str("u"), CredentialRetireAt: &soon}, now))
	far := now.Add(7 * 24 * time.Hour)
	require.Equal(t, rotationWakeupCap, rotationWakeup(store.DatabaseTenant{PreviousLoginRole: str("u"), CredentialRetireAt: &far}, now))
	past := now.Add(-time.Hour)
	require.Equal(t, requeueWait, rotationWakeup(store.DatabaseTenant{PreviousLoginRole: str("u"), CredentialRetireAt: &past}, now))
	require.Equal(t, requeueWait, rotationWakeup(store.DatabaseTenant{PendingLoginRole: str("u")}, now))
}

// The commit: a Secret for the next login role and the pending columns,
// idempotent, refused where the row refuses it.
func TestRotateDatabaseCredentialsCommits(t *testing.T) {
	fx := newSettleFixture(t)
	ctx := context.Background()

	_, err := fx.control.RotateDatabaseCredentials(ctx, fx.envID, "data", time.Hour)
	require.ErrorIs(t, err, ErrDatabaseNotProvisioned)
	_, err = fx.control.RotateDatabaseCredentials(ctx, fx.envID, "nope", time.Hour)
	require.Error(t, err)

	tenant := fx.provisioned(t)
	require.Equal(t, tenant.RoleName, tenant.LoginRole)
	owner := fx.secret(t, tenant.CredentialSecret)
	require.Equal(t, tenant.RoleName, string(owner.Data["username"]))

	before := time.Now()
	result, err := fx.control.RotateDatabaseCredentials(ctx, fx.envID, "data", time.Hour)
	require.NoError(t, err)
	require.Equal(t, tenant.RoleName+"_v2", result.LoginRole)
	require.WithinDuration(t, before.Add(time.Hour), result.RetireAt, 5*time.Second)
	rotated := fx.tenant(t)
	require.NotNil(t, rotated.PendingLoginRole)
	require.Equal(t, result.LoginRole, *rotated.PendingLoginRole)
	require.Equal(t, "dbcred-"+tenant.CredentialSecret[len("dbcred-"):]+"-v2", *rotated.PendingCredentialSecret)
	require.Equal(t, tenant.RoleName, rotated.LoginRole, "the commit does not switch anything by itself")
	require.EqualValues(t, 1, rotated.CredentialVersion)
	pending := fx.secret(t, *rotated.PendingCredentialSecret)
	require.Equal(t, result.LoginRole, string(pending.Data["username"]))
	require.Len(t, pending.Data["password"], 32)
	require.NotEqual(t, owner.Data["password"], pending.Data["password"])

	// Idempotent: the pending role is handed back, no second Secret.
	again, err := fx.control.RotateDatabaseCredentials(ctx, fx.envID, "data", time.Minute)
	require.NoError(t, err)
	require.Equal(t, result.LoginRole, again.LoginRole)
	require.True(t, result.RetireAt.Equal(again.RetireAt), "the original window stands: %s vs %s", result.RetireAt, again.RetireAt)
	require.Equal(t, pending.ResourceVersion, fx.secret(t, *rotated.PendingCredentialSecret).ResourceVersion)
}

// The worker's take: nothing moves until the pool reports the role with
// its password; then the setting is applied, the mirror written, the row
// swapped once and the environment poked.
func TestProvisionTakesPendingLoginRole(t *testing.T) {
	fx := newSettleFixture(t)
	ctx := context.Background()
	tenant := fx.provisioned(t)
	ownerSecret := fx.secret(t, tenant.CredentialSecret)
	result, err := fx.control.RotateDatabaseCredentials(ctx, fx.envID, "data", time.Hour)
	require.NoError(t, err)
	pendingSecret := fx.secret(t, *fx.tenant(t).PendingCredentialSecret)
	pokes := len(*fx.poked)

	// The pool has not created the role yet: the pass waits and leaves
	// the mirror on the owner.
	requeue, _ := fx.pass(t)
	require.Equal(t, requeueWait, requeue)
	require.Contains(t, fx.control.WaitingReason(fx.claim.ID), "waiting for the pool to create it")
	require.Empty(t, fx.fake.scripts())
	require.Equal(t, tenant.RoleName, string(fx.mirror(t).Data["username"]))
	require.EqualValues(t, 1, fx.tenant(t).CredentialVersion)

	// Created but with a stale password version: still waiting.
	fx.fake.reconcileRole(result.LoginRole, "0")
	requeue, _ = fx.pass(t)
	require.Equal(t, requeueWait, requeue)
	require.Contains(t, fx.control.WaitingReason(fx.claim.ID), "apply its password")

	// No ready primary: a wait, not an error, and nothing swapped.
	fx.fake.reconcileRole(result.LoginRole, pendingSecret.ResourceVersion)
	fx.fake.execErr = kube.ErrNoReadyPod
	requeue, _ = fx.pass(t)
	require.Equal(t, requeueWait, requeue)
	require.Contains(t, fx.control.WaitingReason(fx.claim.ID), "no ready primary")
	require.EqualValues(t, 1, fx.tenant(t).CredentialVersion)

	// Taken: the setting, the mirror, the swap, the poke.
	fx.fake.execErr = nil
	requeue, phase := fx.pass(t)
	require.Equal(t, claim.PhaseProvisioned, phase)
	require.Empty(t, fx.control.WaitingReason(fx.claim.ID))
	scripts := fx.fake.scripts()
	require.Len(t, scripts, 2, "the refused attempt and the taken one")
	require.Equal(t, cnpg.TakeLoginRoleSQL(result.LoginRole, tenant.RoleName), scripts[1])
	require.Equal(t, cnpg.PrimarySelector("pg17-shared"), fx.fake.execs[1].selector)
	require.Equal(t, "postgres", fx.fake.execs[1].container)
	taken := fx.tenant(t)
	require.EqualValues(t, 2, taken.CredentialVersion)
	require.Equal(t, result.LoginRole, taken.LoginRole)
	require.Equal(t, pendingSecret.Name, taken.CredentialSecret)
	require.Nil(t, taken.PendingLoginRole)
	require.Equal(t, tenant.RoleName, *taken.PreviousLoginRole)
	require.Equal(t, ownerSecret.Name, *taken.PreviousCredentialSecret)
	require.Greater(t, len(*fx.poked), pokes, "consumers roll off the version bump")
	mirror := fx.mirror(t)
	require.Equal(t, result.LoginRole, string(mirror.Data["username"]))
	require.Equal(t, pendingSecret.Data["password"], mirror.Data["password"])
	require.Contains(t, string(mirror.Data["url"]), "postgresql://"+result.LoginRole+":")
	require.Greater(t, requeue, time.Duration(0), "the worker wakes itself for the retirement")
	require.LessOrEqual(t, requeue, rotationWakeupCap)

	// Inside the window a pass changes nothing: no second swap, no
	// retirement, no SQL (the setting persists in the pool), the owner
	// still logs in.
	requeue, _ = fx.pass(t)
	require.Len(t, fx.fake.scripts(), 2, "nothing runs again once the role is taken")
	require.EqualValues(t, 2, fx.tenant(t).CredentialVersion)
	require.NotNil(t, fx.tenant(t).PreviousLoginRole)
	_, err = fx.fake.GetSecret(ctx, Namespace, ownerSecret.Name)
	require.NoError(t, err, "the previous Secret stays while the window is open")
}

// The retirement: past the deadline the previous Secret is deleted, the
// owner loses its login (first rotation) or the login role is dropped
// (later ones), and the row forgets it. Teardown drops everything.
func TestProvisionRetiresPreviousLoginRole(t *testing.T) {
	fx := newSettleFixture(t)
	ctx := context.Background()
	tenant := fx.provisioned(t)
	ownerSecret := tenant.CredentialSecret

	take := func(t *testing.T) DatabaseRotation {
		t.Helper()
		result, err := fx.control.RotateDatabaseCredentials(ctx, fx.envID, "data", time.Hour)
		require.NoError(t, err)
		pending := fx.secret(t, *fx.tenant(t).PendingCredentialSecret)
		fx.fake.reconcileRole(result.LoginRole, pending.ResourceVersion)
		fx.pass(t)
		require.Equal(t, result.LoginRole, fx.tenant(t).LoginRole)
		return result
	}
	v2 := take(t)
	require.NoError(t, fx.control.RetireDatabaseCredentials(ctx, fx.envID, "data"))
	require.False(t, time.Now().Before(*fx.tenant(t).CredentialRetireAt))

	// A second commit inside the window is refused until the retirement.
	_, err := fx.control.RotateDatabaseCredentials(ctx, fx.envID, "data", time.Hour)
	require.ErrorIs(t, err, ErrRotationInFlight)

	requeue, _ := fx.pass(t)
	require.Zero(t, requeue, "nothing left to wake for")
	retired := fx.tenant(t)
	require.Nil(t, retired.PreviousLoginRole)
	require.Nil(t, retired.PreviousCredentialSecret)
	require.Nil(t, retired.CredentialRetireAt)
	require.Equal(t, v2.LoginRole, retired.LoginRole)
	_, err = fx.fake.GetSecret(ctx, Namespace, ownerSecret)
	require.Error(t, err, "the owner's Secret is gone")
	require.Contains(t, fx.fake.deleted, ownerSecret)
	scripts := fx.fake.scripts()
	require.Equal(t, cnpg.RetireOwnerLoginSQL(tenant.RoleName), scripts[len(scripts)-1],
		"the first rotation closes the owner's login and drops nothing")
	for _, script := range scripts {
		require.NotContains(t, script, "DROP ROLE")
	}

	// The second rotation retires a login role: still managed by the pool
	// (a stale status) it waits; released, it is dropped in the tenant's
	// database.
	v3 := take(t)
	require.NoError(t, fx.control.RetireDatabaseCredentials(ctx, fx.envID, "data"))
	requeue, _ = fx.pass(t)
	require.Equal(t, requeueWait, requeue)
	require.Contains(t, fx.control.WaitingReason(fx.claim.ID), "waiting for the pool to release it")
	require.NotNil(t, fx.tenant(t).PreviousLoginRole)
	fx.fake.mu.Lock()
	delete(fx.fake.roleStatus, v2.LoginRole)
	fx.fake.mu.Unlock()
	requeue, _ = fx.pass(t)
	require.Zero(t, requeue)
	require.Nil(t, fx.tenant(t).PreviousLoginRole)
	require.Equal(t, v3.LoginRole, fx.tenant(t).LoginRole)
	last := fx.fake.execs[len(fx.fake.execs)-1]
	require.Equal(t, cnpg.RetireLoginRoleSQL(v2.LoginRole, tenant.RoleName), last.command[len(last.command)-1])
	require.Equal(t, tenant.DatabaseName, last.command[len(last.command)-3], "objects a reset role created live in the tenant's database")

	// Teardown: every Secret deleted, every role dropped, login roles
	// before the owner, waiting for the pool to release them first.
	currentSecret := fx.tenant(t).CredentialSecret
	_, err = fx.db.ReleaseClaim(ctx, fx.claim.ID)
	require.NoError(t, err)
	requeue, _ = fx.pass(t)
	require.Equal(t, requeueWait, requeue, "the pool still lists the current role")
	fx.fake.mu.Lock()
	fx.fake.roleStatus = nil
	fx.fake.mu.Unlock()
	_, phase := fx.pass(t)
	require.Equal(t, claim.PhaseReleased, phase)
	drop := fx.fake.scripts()[len(fx.fake.scripts())-1]
	require.Equal(t, cnpg.DropTenantRolesSQL([]string{v3.LoginRole, tenant.RoleName}), drop)
	require.Less(t, strings.Index(drop, v3.LoginRole), strings.Index(drop, `DROP ROLE "`+tenant.RoleName+`"`))
	require.Contains(t, fx.fake.deleted, currentSecret)
}

// The filer's system claim takes its username from the Secret, so a
// rotated metadata database would reach it; and an unconverted tenant's
// mirror still names the owner.
func TestOutputMirrorUsernameComesFromTheSecret(t *testing.T) {
	fx := newSettleFixture(t)
	tenant := fx.provisioned(t)
	mirror := fx.mirror(t)
	require.Equal(t, tenant.RoleName, string(mirror.Data["username"]))
	require.Equal(t, fx.secret(t, tenant.CredentialSecret).Data["password"], mirror.Data["password"])
}
