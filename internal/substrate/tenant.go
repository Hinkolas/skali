package substrate

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/Hinkolas/skali/internal/claim"
	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/substrate/cnpg"
	"github.com/Hinkolas/skali/internal/utils"
)

// reconcileClaim drives one claim toward its phase goal. Waiting states
// requeue with a visible reason instead of failing; there is no failed
// phase.
func (c *Controller) reconcileClaim(ctx context.Context, id uuid.UUID) (time.Duration, error) {
	row, err := c.deps.DB.GetClaim(ctx, id)
	if err != nil {
		if errors.Is(err, dbstore.ErrNotFound) {
			return 0, nil
		}
		return 0, err
	}

	switch claim.Phase(row.Phase) {
	case claim.PhaseReleased:
		c.setWaiting(id, "")
		c.clearExtensions(id)
		c.publishClaim(*row)
		return 0, nil
	case claim.PhaseReleasing:
		return c.teardownClaim(ctx, *row)
	}

	transitioned, wakeup, err := c.provision(ctx, *row)
	requeue := time.Duration(0)
	switch waiting, ok := errors.AsType[errWaiting](err); {
	case ok:
		c.setWaiting(id, waiting.reason)
		requeue = requeueWait
	case err != nil:
		return 0, err
	default:
		c.setWaiting(id, "")
		// The pass applied the row it read at the top; only that encoding
		// settles a pending extension change.
		c.settleExtensions(id, row.Extensions)
		// A rotation in flight wakes the worker again on its own: for the
		// retirement at the window's end, nothing else is guaranteed to.
		requeue = wakeup
	}

	// Publish the fresh phase before poking the environment so its next
	// pass evaluates against the new truth.
	current, err := c.deps.DB.GetClaim(ctx, id)
	if err != nil {
		return 0, err
	}
	c.publishClaim(*current)
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

// provision walks a pending/bound/provisioned claim through placement, pool
// readiness, tenant objects, the credential rotation steps and the output
// mirror. Every step is idempotent, so provisioned claims re-run it as
// drift repair. It reports whether the claim reached provisioned in this
// pass and when the worker must look again on its own (a retirement
// ahead), zero when nothing is scheduled.
func (c *Controller) provision(ctx context.Context, row store.DatabaseClaim) (bool, time.Duration, error) {
	now := time.Now()
	pool, err := c.place(ctx, row)
	if err != nil {
		return false, 0, err
	}
	if err := c.ensureNamespace(ctx); err != nil {
		return false, 0, err
	}

	tenant, err := c.ensureTenantRecord(ctx, row, pool)
	if err != nil {
		return false, 0, err
	}
	credential, err := c.ensureCredentialSecret(ctx, row, *tenant)
	if err != nil {
		return false, 0, err
	}
	wait, err := c.ensurePool(ctx, *pool, now)
	if err != nil {
		return false, 0, err
	}
	if wait > 0 {
		return false, 0, errWaiting{reason: "waiting for database node memory to size the pool"}
	}
	ready, reason, err := c.poolReady(ctx, *pool)
	if err != nil {
		return false, 0, err
	}
	if !ready {
		return false, 0, errWaiting{reason: reason}
	}
	if err := c.ensureDatabaseObject(ctx, row, *pool, *tenant); err != nil {
		return false, 0, err
	}
	applied, reason, err := c.databaseApplied(ctx, *tenant)
	if err != nil {
		return false, 0, err
	}
	if !applied {
		return false, 0, errWaiting{reason: reason}
	}
	// A committed rotation is taken here, after the pool holds the new
	// login role and before the mirror is written from the current one.
	tenant, err = c.takePendingLoginRole(ctx, row, *pool, *tenant)
	if err != nil {
		return false, 0, err
	}
	if tenant.CredentialSecret != credential.Name {
		if credential, err = c.ensureCredentialSecret(ctx, row, *tenant); err != nil {
			return false, 0, err
		}
	}
	if err := c.ensureOutputMirror(ctx, row, *tenant, credential); err != nil {
		return false, 0, err
	}
	tenant, err = c.retirePreviousLoginRole(ctx, row, *pool, *tenant, now)
	if err != nil {
		return false, 0, err
	}

	// place may have bound the claim mid-pass; the transition test needs the
	// fresh phase or the pass that binds and completes can never settle.
	current, err := c.deps.DB.GetClaim(ctx, row.ID)
	if err != nil {
		return false, 0, err
	}
	if claim.Phase(current.Phase) == claim.PhaseBound {
		if _, err := c.deps.DB.TransitionClaim(ctx, row.ID, claim.PhaseProvisioned); err != nil {
			return false, 0, err
		}
		return true, 0, nil
	}
	return false, rotationWakeup(*tenant, now), nil
}

// ensureTenantRecord generates and durably records the tenant identity once.
func (c *Controller) ensureTenantRecord(ctx context.Context, row store.DatabaseClaim, pool *store.DatabaseCluster) (*store.DatabaseTenant, error) {
	suffix := utils.ShortID(row.ID)
	base := sqlName(ownerBase(row))
	return c.deps.DB.RecordTenant(ctx, dbstore.TenantInput{
		ClaimID:          row.ID,
		ClusterID:        pool.ID,
		DatabaseName:     "db_" + base + "_" + suffix,
		RoleName:         "u_" + base + "_" + suffix,
		CredentialSecret: "dbcred-" + suffix,
		Host:             pool.Name + "-rw." + Namespace + ".svc.cluster.local",
		Port:             5432,
	})
}

// ensureCredentialSecret creates the login role's basic-auth Secret on
// first provisioning and returns the Secret as the cluster holds it (the
// username is the login role, the password exists only there; neither is
// logged or persisted elsewhere).
func (c *Controller) ensureCredentialSecret(ctx context.Context, row store.DatabaseClaim, tenant store.DatabaseTenant) (*corev1.Secret, error) {
	existing, err := c.deps.Cluster.GetSecret(ctx, Namespace, tenant.CredentialSecret)
	if err == nil {
		return existing, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("substrate: read credential secret: %w", err)
	}
	password, err := generatePassword()
	if err != nil {
		return nil, err
	}
	secret := cnpg.RenderCredentialSecret(Namespace, tenant.CredentialSecret,
		tenantPool(tenant), tenant.LoginRole, password, claimLabels(row))
	if _, err := c.deps.Cluster.ApplyAs(ctx, secret, kube.FieldManagerPlatform, false); err != nil {
		return nil, fmt.Errorf("substrate: apply credential secret: %w", err)
	}
	applied, err := c.deps.Cluster.GetSecret(ctx, Namespace, tenant.CredentialSecret)
	if err != nil {
		return nil, fmt.Errorf("substrate: read credential secret: %w", err)
	}
	return applied, nil
}

func (c *Controller) ensureDatabaseObject(ctx context.Context, row store.DatabaseClaim, pool store.DatabaseCluster, tenant store.DatabaseTenant) error {
	object := cnpg.RenderDatabase(cnpg.DatabaseSpec{
		Namespace:    Namespace,
		ObjectName:   databaseObjectName(tenant),
		ClusterName:  pool.Name,
		DatabaseName: tenant.DatabaseName,
		Owner:        tenant.RoleName,
		Extensions:   dbstore.Extensions(row),
		Labels:       claimLabels(row),
	})
	if _, err := c.deps.Cluster.ApplyAs(ctx, object, kube.FieldManagerPlatform, false); err != nil {
		return fmt.Errorf("substrate: apply database: %w", err)
	}
	return nil
}

// databaseApplied reports whether CNPG reconciled the tenant's Database CR
// at its current generation, so a just-changed spec (extension updates,
// teardown's ensure absent) never passes on a stale applied flag.
func (c *Controller) databaseApplied(ctx context.Context, tenant store.DatabaseTenant) (bool, string, error) {
	object, err := c.deps.Cluster.GetObject(ctx, cnpg.DatabaseGVR, Namespace, databaseObjectName(tenant))
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, "database object not created yet", nil
		}
		return false, "", fmt.Errorf("substrate: read database: %w", err)
	}
	applied, found, _ := unstructured.NestedBool(object.Object, "status", "applied")
	observedGeneration, _, _ := unstructured.NestedInt64(object.Object, "status", "observedGeneration")
	if !found || observedGeneration < object.GetGeneration() {
		return false, fmt.Sprintf("database %s: waiting for reconciliation", tenant.DatabaseName), nil
	}
	if !applied {
		message, _, _ := unstructured.NestedString(object.Object, "status", "message")
		if message == "" {
			message = "not applied yet"
		}
		return false, fmt.Sprintf("database %s: %s", tenant.DatabaseName, message), nil
	}
	return true, "", nil
}

// ensureOutputMirror writes the service claim's connection outputs into its
// environment namespace from a credential Secret (its username is the
// login role). System claims publish outputs through the internal claim
// API instead.
func (c *Controller) ensureOutputMirror(ctx context.Context, row store.DatabaseClaim, tenant store.DatabaseTenant, credential *corev1.Secret) error {
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
	username := string(credential.Data[corev1.BasicAuthUsernameKey])
	password := string(credential.Data[corev1.BasicAuthPasswordKey])
	port := strconv.Itoa(int(tenant.Port))
	url := "postgresql://" + username + ":" + password + "@" +
		tenant.Host + ":" + port + "/" + tenant.DatabaseName
	secret := kubernetes.RenderOutputSecret(project, environment, environmentID,
		"databases", service, map[string][]byte{
			"host":     []byte(tenant.Host),
			"port":     []byte(port),
			"name":     []byte(tenant.DatabaseName),
			"username": []byte(username),
			"password": []byte(password),
			"url":      []byte(url),
		})
	if _, err := c.deps.Cluster.ApplyAs(ctx, secret, kube.FieldManagerPlatform, false); err != nil {
		if apierrors.IsNotFound(err) {
			// The environment namespace is created by the environment
			// reconciler; until it exists the claim visibly waits.
			return errWaiting{reason: "environment namespace not created yet"}
		}
		return fmt.Errorf("substrate: apply output mirror: %w", err)
	}
	return nil
}

// ownerBase is the human part of generated tenant identities.
func ownerBase(row store.DatabaseClaim) string {
	if row.OwnerKind == dbstore.OwnerSystem {
		return row.SystemKey
	}
	return row.ServiceKey
}

// claimLabels stamps substrate objects with the claim's identity so the
// dynamic observation source can index them back to their owner.
func claimLabels(row store.DatabaseClaim) map[string]string {
	labels := map[string]string{kubernetes.LabelClaim: row.ID.String()}
	if row.OwnerKind == dbstore.OwnerService && row.EnvironmentID != nil {
		labels[kubernetes.LabelEnvironment] = row.EnvironmentID.String()
		labels[kubernetes.LabelService] = "databases." + row.ServiceKey
	}
	return labels
}

func databaseObjectName(tenant store.DatabaseTenant) string {
	return "db-" + utils.ShortID(tenant.ClaimID)
}

// tenantPool recovers the pool name from the tenant host
// ("<pool>-rw.<namespace>....") for labeling without another lookup.
func tenantPool(tenant store.DatabaseTenant) string {
	if pool, _, found := strings.Cut(tenant.Host, "-rw."); found {
		return pool
	}
	return tenant.Host
}

// sqlName reduces an owner key to a safe SQL identifier fragment.
func sqlName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-', r == '_', r == '/', r == '.':
			b.WriteByte('_')
		}
	}
	result := strings.Trim(b.String(), "_")
	if result == "" {
		result = "db"
	}
	if len(result) > 24 {
		result = result[:24]
	}
	return result
}

const passwordAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// generatePassword returns a 32-character random password from an
// URL/DSN-safe alphabet.
func generatePassword() (string, error) {
	var b strings.Builder
	for range 32 {
		index, err := rand.Int(rand.Reader, big.NewInt(int64(len(passwordAlphabet))))
		if err != nil {
			return "", fmt.Errorf("substrate: generate password: %w", err)
		}
		b.WriteByte(passwordAlphabet[index.Int64()])
	}
	return b.String(), nil
}
