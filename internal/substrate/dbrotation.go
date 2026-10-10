package substrate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/Hinkolas/skali/internal/claim"
	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/substrate/cnpg"
	"github.com/Hinkolas/skali/internal/utils"
)

// Database credential rotation. A PostgreSQL role holds one password and
// ownership cannot be shared, so the overlap a rotation needs comes from
// two login roles under one owner: role_name owns the database and, from
// the first rotation on, never logs in again; login roles named after the
// credential version alternate as its members, each session acting as the
// owner (cnpg.TakeLoginRoleSQL) so what the application creates belongs to
// the owner and a retired login role is dropped without a trace.
//
// The tenant row is the truth (which role is current, pending, previous
// and when the previous retires); the Secrets hold the passwords; the
// Cluster spec is rendered from the row (tenantRoles). The API commits only
// the pending role (RotateDatabaseCredentials). Everything after runs in
// the claim worker's provision pass, idempotent and level-triggered:
// takePendingLoginRole once the pool holds the role, retirePreviousLoginRole
// once the window has passed.

var (
	// ErrDatabaseNotProvisioned refuses a rotation before the claim has
	// provisioned: there is no role to rotate yet.
	ErrDatabaseNotProvisioned = errors.New("substrate: the database is not provisioned")
	// ErrRotationInFlight refuses a rotation while the previous login role
	// of an earlier one is still retiring; the rotation run retires it
	// first and retries.
	ErrRotationInFlight = errors.New("substrate: the previous credentials are still retiring")
)

// DatabaseRotation is what a committed rotation hands back: the login role
// the consumers switch to (its password stays in the Secret) and when the
// current one retires.
type DatabaseRotation struct {
	LoginRole string
	RetireAt  time.Time
}

// rotationWakeupCap bounds how long the worker sleeps on a rotation in
// flight; a pass also re-applies the pool, so a 7-day window is not polled
// every minute.
const rotationWakeupCap = 5 * time.Minute

// RotateDatabaseCredentials commits a rotation for one service's database:
// the next login role is named, its password Secret applied, and the row
// records it as pending with the instant the current role retires. The
// claim worker takes it from there. Idempotent: a pending role already
// committed is returned again.
func (c *Controller) RotateDatabaseCredentials(ctx context.Context, environmentID uuid.UUID, serviceKey string, retireAfter time.Duration) (DatabaseRotation, error) {
	row, err := c.deps.DB.LiveServiceClaim(ctx, environmentID, serviceKey)
	if err != nil {
		return DatabaseRotation{}, fmt.Errorf("substrate: resolve database claim for %s: %w", serviceKey, err)
	}
	if claim.Phase(row.Phase) != claim.PhaseProvisioned {
		return DatabaseRotation{}, ErrDatabaseNotProvisioned
	}
	tenant, err := c.deps.DB.LiveTenant(ctx, row.ID)
	if err != nil {
		if errors.Is(err, dbstore.ErrNotFound) {
			return DatabaseRotation{}, ErrDatabaseNotProvisioned
		}
		return DatabaseRotation{}, err
	}
	if tenant.PendingLoginRole != nil && tenant.CredentialRetireAt != nil {
		c.EnqueueClaim(row.ID, reasonRotation)
		return DatabaseRotation{LoginRole: *tenant.PendingLoginRole, RetireAt: *tenant.CredentialRetireAt}, nil
	}
	if tenant.PreviousLoginRole != nil {
		return DatabaseRotation{}, ErrRotationInFlight
	}

	version := tenant.CredentialVersion + 1
	login := cnpg.LoginRoleName(tenant.RoleName, version)
	if !cnpg.ValidRoleName(login) {
		return DatabaseRotation{}, fmt.Errorf("substrate: login role name %q is not usable", login)
	}
	secretName := credentialSecretName(*tenant, version)
	password, err := generatePassword()
	if err != nil {
		return DatabaseRotation{}, err
	}
	// Applying under the platform manager makes a retry after a crash
	// overwrite an orphan of the same name: nobody has held its password.
	secret := cnpg.RenderCredentialSecret(Namespace, secretName, tenantPool(*tenant), login, password, claimLabels(*row))
	if _, err := c.deps.Cluster.ApplyAs(ctx, secret, kube.FieldManagerPlatform, false); err != nil {
		return DatabaseRotation{}, fmt.Errorf("substrate: apply credential secret: %w", err)
	}
	retireAt := time.Now().Add(retireAfter).UTC().Truncate(time.Second)
	set, err := c.deps.DB.SetTenantPendingCredential(ctx, tenant.ID, login, secretName, retireAt)
	if err != nil {
		return DatabaseRotation{}, err
	}
	if !set {
		// Lost a race with another commit: re-read and report what stuck.
		return c.RotateDatabaseCredentials(ctx, environmentID, serviceKey, retireAfter)
	}
	slog.Info("substrate: database credential rotation committed",
		"service", serviceKey, "database", tenant.DatabaseName, "loginRole", login, "retireAt", retireAt.Format(time.RFC3339))
	c.EnqueueClaim(row.ID, reasonRotation)
	return DatabaseRotation{LoginRole: login, RetireAt: retireAt}, nil
}

// RetireDatabaseCredentials moves the previous login role's retirement to
// now and wakes the worker; a no-op when nothing is retiring.
func (c *Controller) RetireDatabaseCredentials(ctx context.Context, environmentID uuid.UUID, serviceKey string) error {
	row, err := c.deps.DB.LiveServiceClaim(ctx, environmentID, serviceKey)
	if err != nil {
		return fmt.Errorf("substrate: resolve database claim for %s: %w", serviceKey, err)
	}
	tenant, err := c.deps.DB.LiveTenant(ctx, row.ID)
	if err != nil {
		return err
	}
	if err := c.deps.DB.RetireTenantCredentials(ctx, tenant.ID); err != nil {
		return err
	}
	c.EnqueueClaim(row.ID, reasonRotation)
	return nil
}

// credentialSecretName names the Secret of the login role taken at a
// credential version; the first one (the owner's) keeps its historical
// name.
func credentialSecretName(tenant store.DatabaseTenant, version int64) string {
	return fmt.Sprintf("dbcred-%s-v%d", utils.ShortID(tenant.ClaimID), version)
}

// takePendingLoginRole makes a committed login role the current one, in
// the order that keeps every holder of a credential able to connect: the
// pool must report the role in place with its password, the role is made
// to act as the owner, the mirror carries the new pair, and only then the
// row swaps and the version advances (exactly once) so the consumers
// roll. It returns the row as it stands afterwards.
func (c *Controller) takePendingLoginRole(ctx context.Context, row store.DatabaseClaim, pool store.DatabaseCluster, tenant store.DatabaseTenant) (*store.DatabaseTenant, error) {
	if tenant.PendingLoginRole == nil || tenant.PendingCredentialSecret == nil {
		return &tenant, nil
	}
	login := *tenant.PendingLoginRole
	secret, err := c.deps.Cluster.GetSecret(ctx, Namespace, *tenant.PendingCredentialSecret)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, errWaiting{reason: "credential secret of the pending login role is missing"}
		}
		return nil, fmt.Errorf("substrate: read pending credential secret: %w", err)
	}
	cluster, err := c.deps.Cluster.GetObject(ctx, cnpg.ClusterGVR, Namespace, pool.Name)
	if err != nil {
		return nil, fmt.Errorf("substrate: read pool %s: %w", pool.Name, err)
	}
	if ready, reason := cnpg.RoleReconciled(cluster, login, secret.ResourceVersion); !ready {
		return nil, errWaiting{reason: reason}
	}
	if err := c.execPrimarySQL(ctx, pool, "postgres", cnpg.TakeLoginRoleSQL(login, tenant.RoleName)); err != nil {
		return nil, err
	}
	if err := c.ensureOutputMirror(ctx, row, tenant, secret); err != nil {
		return nil, err
	}
	took, err := c.deps.DB.BeginTenantCredentialRotation(ctx, tenant.ID)
	if err != nil {
		return nil, err
	}
	current, err := c.deps.DB.LiveTenant(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	if took {
		slog.Info("substrate: database login role taken", "database", tenant.DatabaseName,
			"loginRole", login, "version", current.CredentialVersion)
		if c.deps.Enqueue != nil && row.EnvironmentID != nil {
			c.deps.Enqueue(*row.EnvironmentID)
		}
	}
	return current, nil
}

// roleSettleDelay is how long a drop waits after the role left the Cluster
// spec before it runs: the operator's synchronizer only acts on
// configuration changes and one still carrying the previous spec can land
// a few seconds after the apply, recreating what it listed; by then it
// has seen the new spec. Tests shorten it.
var roleSettleDelay = 15 * time.Second

// settled reports whether a drop keyed by id has waited out the settle
// delay since it was first attempted; the first call starts the clock.
// The clock lives in memory only: a restart waits once more, which is
// harmless.
func (c *Controller) settled(id uuid.UUID, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.settling == nil {
		c.settling = map[uuid.UUID]time.Time{}
	}
	since, ok := c.settling[id]
	if !ok {
		c.settling[id] = now
		return roleSettleDelay <= 0
	}
	return !now.Before(since.Add(roleSettleDelay))
}

func (c *Controller) forgetSettle(id uuid.UUID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.settling, id)
}

// retirePreviousLoginRole retires the previous login role once its window
// has passed and returns the row as it stands afterwards. This pass's pool
// apply already left the role out of the Cluster spec (an owner is listed
// without a login and with its password wiped), and its Secret is gone.
// The operator closes the owner's login itself from that spec; the worker
// waits for it and then only terminates the owner's sessions, so a
// synchronizer still carrying the previous spec can never reopen what the
// worker closed. Any other login role is dropped by SQL once the spec has
// settled and confirmed gone on the next pass. Every step is idempotent.
func (c *Controller) retirePreviousLoginRole(ctx context.Context, row store.DatabaseClaim, pool store.DatabaseCluster, tenant store.DatabaseTenant, now time.Time) (*store.DatabaseTenant, error) {
	if tenant.PreviousLoginRole == nil {
		return &tenant, nil
	}
	if tenant.CredentialRetireAt != nil && now.Before(*tenant.CredentialRetireAt) {
		return &tenant, nil
	}
	previous := *tenant.PreviousLoginRole
	if tenant.PreviousCredentialSecret != nil {
		if _, err := c.deps.Cluster.Delete(ctx, kube.ObjectRef{
			GVK: secretGVK, Namespace: Namespace, Name: *tenant.PreviousCredentialSecret,
		}); err != nil {
			return nil, fmt.Errorf("substrate: delete retired credential secret: %w", err)
		}
	}
	login, err := c.queryPrimary(ctx, pool, "postgres", cnpg.RoleLoginQuery(previous))
	if err != nil {
		return nil, err
	}
	if previous == tenant.RoleName {
		if login != "f" {
			return nil, errWaiting{reason: fmt.Sprintf("role %s: waiting for the pool to close its login", previous)}
		}
		if err := c.execPrimarySQL(ctx, pool, "postgres", cnpg.RetireOwnerLoginSQL(previous)); err != nil {
			return nil, err
		}
	} else if login != "" {
		if !c.settled(tenant.ID, now) {
			return nil, errWaiting{reason: fmt.Sprintf("role %s: waiting for the pool to settle", previous)}
		}
		if err := c.execPrimarySQL(ctx, pool, tenant.DatabaseName, cnpg.RetireLoginRoleSQL(previous, tenant.RoleName)); err != nil {
			return nil, err
		}
		return nil, errWaiting{reason: fmt.Sprintf("role %s: dropped, confirming on the next pass", previous)}
	}
	if err := c.deps.DB.FinishTenantCredentialRotation(ctx, tenant.ID); err != nil {
		return nil, err
	}
	c.forgetSettle(tenant.ID)
	slog.Info("substrate: database login role retired", "database", tenant.DatabaseName, "loginRole", previous)
	return c.deps.DB.LiveTenant(ctx, row.ID)
}

// rotationWakeup is when the worker must look at a rotating tenant again
// on its own: at the retirement, capped so a long window is not polled
// tightly; zero when nothing is scheduled.
func rotationWakeup(tenant store.DatabaseTenant, now time.Time) time.Duration {
	if tenant.PreviousLoginRole == nil && tenant.PendingLoginRole == nil {
		return 0
	}
	wait := requeueWait
	if tenant.CredentialRetireAt != nil {
		wait = tenant.CredentialRetireAt.Sub(now)
	}
	return min(max(wait, requeueWait), rotationWakeupCap)
}

// sqlExecTimeout bounds one script or query in a pool's primary: the role
// changes are small, and an exec whose stream wedged must not hold the
// claim's worker.
const sqlExecTimeout = 30 * time.Second

// execPrimarySQL runs one script as the postgres superuser in the pool's
// primary instance. No ready primary (a failover, a restart) and a script
// the server refused both surface as waits with their reason; the scripts
// are idempotent and the pass retries.
func (c *Controller) execPrimarySQL(ctx context.Context, pool store.DatabaseCluster, database, script string) error {
	ctx, cancel := context.WithTimeout(ctx, sqlExecTimeout)
	defer cancel()
	_, err := c.deps.Cluster.ExecInPod(ctx, Namespace, cnpg.PrimarySelector(pool.Name), cnpg.PostgresContainer,
		cnpg.PSQLCommand(database, script))
	if err == nil {
		return nil
	}
	if errors.Is(err, kube.ErrNoReadyPod) {
		return errWaiting{reason: fmt.Sprintf("pool %s has no ready primary", pool.Name)}
	}
	return errWaiting{reason: fmt.Sprintf("pool %s refused a role change: %s", pool.Name, errorTail(err))}
}

// queryPrimary runs one query in the pool's primary and returns its bare
// result, trimmed; failures surface as waits like execPrimarySQL's.
func (c *Controller) queryPrimary(ctx context.Context, pool store.DatabaseCluster, database, query string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, sqlExecTimeout)
	defer cancel()
	out, err := c.deps.Cluster.ExecInPod(ctx, Namespace, cnpg.PrimarySelector(pool.Name), cnpg.PostgresContainer,
		cnpg.PSQLQuery(database, query))
	if err != nil {
		if errors.Is(err, kube.ErrNoReadyPod) {
			return "", errWaiting{reason: fmt.Sprintf("pool %s has no ready primary", pool.Name)}
		}
		return "", errWaiting{reason: fmt.Sprintf("pool %s refused a role query: %s", pool.Name, errorTail(err))}
	}
	return strings.TrimSpace(out), nil
}

// errorTail keeps the last line of an exec error, where psql puts its
// message, bounded for the waiting reason.
func errorTail(err error) string {
	text := strings.TrimSpace(err.Error())
	if i := strings.LastIndex(text, "\n"); i >= 0 {
		text = strings.TrimSpace(text[i+1:])
	}
	if len(text) > 200 {
		text = text[:200]
	}
	return text
}

// tenantRoleNames lists every role a tenant may hold on its pool, login
// roles before the owner: the drop order at teardown.
func tenantRoleNames(tenant store.DatabaseTenant) []string {
	var names []string
	add := func(name string) {
		if name == "" || name == tenant.RoleName {
			return
		}
		for _, seen := range names {
			if seen == name {
				return
			}
		}
		names = append(names, name)
	}
	if tenant.PendingLoginRole != nil {
		add(*tenant.PendingLoginRole)
	}
	add(tenant.LoginRole)
	if tenant.PreviousLoginRole != nil {
		add(*tenant.PreviousLoginRole)
	}
	return append(names, tenant.RoleName)
}

// tenantSecretNames lists every credential Secret a tenant may hold,
// including the one a crashed commit may have left for the next version.
func tenantSecretNames(tenant store.DatabaseTenant) []string {
	names := []string{tenant.CredentialSecret, credentialSecretName(tenant, tenant.CredentialVersion+1)}
	if tenant.PendingCredentialSecret != nil {
		names = append(names, *tenant.PendingCredentialSecret)
	}
	if tenant.PreviousCredentialSecret != nil {
		names = append(names, *tenant.PreviousCredentialSecret)
	}
	return names
}
