package substrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/edge"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/layout"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/substrate/seaweed"
)

// MetadataClaimKey is the object-storage subsystem's system database claim:
// the filer's metadata store is an ordinary internal
// consumer of the database substrate, there is no object-storage-specific
// provisioner.
const MetadataClaimKey = "object-storage/metadata"

// metadataClaimSpec places the filer store on the shared pool (10.3:
// "normally the shared pool"); the pool's own tier carries availability.
func metadataClaimSpec() dbstore.ClaimSpec {
	return dbstore.ClaimSpec{
		Engine:       DefaultEngine,
		Major:        DefaultMajor,
		Isolation:    dbstore.ClassShared,
		Availability: "single",
		StorageBytes: int64(1) << 30,
	}
}

var (
	deploymentsGVR  = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	statefulSetsGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"}
)

// ensureObjectStoreRow returns the live store row, creating it when the
// installation can hold one: production derives the shape from the
// object-storage node count, dev fixes the single all-in-one. errWaiting
// when the fleet cannot be sized yet.
func (c *Controller) ensureObjectStoreRow(ctx context.Context) (*store.ObjectStore, error) {
	row, err := c.deps.DB.LiveObjectStore(ctx)
	if err == nil {
		return row, nil
	}
	if !errors.Is(err, dbstore.ErrNotFound) {
		return nil, err
	}
	input := dbstore.StoreInput{
		Name:               seaweed.StoreName,
		Masters:            1,
		VolumeServers:      1,
		Replication:        seaweed.ReplicationForNodes(1),
		VolumeStorageBytes: int64(2) << 30, // the dev all-in-one PVC
		Image:              seaweed.Image,
	}
	if c.cfg.Managed {
		capable := len(c.deps.Observed.CapableNodes(layout.CapabilityObjectStorage))
		if capable == 0 {
			return nil, errWaiting{"waiting for object-storage capable nodes"}
		}
		input = desiredShape(capable)
	}
	row, err = c.deps.DB.CreateObjectStore(ctx, input)
	if err != nil {
		return nil, err
	}
	slog.Info("substrate: object store created", "masters", input.Masters,
		"volumeServers", input.VolumeServers, "replication", input.Replication)
	return row, nil
}

// desiredShape derives the managed store's topology from the capable node
// count: a raft quorum of three masters once three nodes carry the
// capability, one volume server per capable node, and one replica on a
// different node once there are two. Production volume servers use the
// node's disk directly; the desired-shape row records no PVC size.
func desiredShape(capable int) dbstore.StoreInput {
	return dbstore.StoreInput{
		Name:          seaweed.StoreName,
		Masters:       seaweed.MastersForNodes(capable),
		VolumeServers: capable,
		Replication:   seaweed.ReplicationForNodes(capable),
		Image:         seaweed.Image,
	}
}

// growShape is the grow-only topology rule: the recorded shape takes every
// dimension the desired one is larger in (more masters, more volume
// servers, a replica) and keeps the rest. Shrinking is never automatic: a
// smaller fleet keeps the recorded shape, reported as under-replication
// and missing members until an operator adjusts it. The second result
// reports whether anything grew, the third whether the fleet is below the
// recorded shape.
func growShape(current store.ObjectStore, desired dbstore.StoreInput) (dbstore.StoreInput, bool, bool) {
	next := dbstore.StoreInput{
		Name:               current.Name,
		Masters:            int(current.Masters),
		VolumeServers:      int(current.VolumeServers),
		Replication:        current.Replication,
		VolumeStorageBytes: current.VolumeStorageBytes,
		Image:              current.Image,
	}
	grew, below := false, false
	if desired.Masters > next.Masters {
		next.Masters, grew = desired.Masters, true
	} else if desired.Masters < next.Masters {
		below = true
	}
	if desired.VolumeServers > next.VolumeServers {
		next.VolumeServers, grew = desired.VolumeServers, true
	} else if desired.VolumeServers < next.VolumeServers {
		below = true
	}
	if desired.Replication > next.Replication {
		next.Replication, grew = desired.Replication, true
	} else if desired.Replication < next.Replication {
		below = true
	}
	return next, grew, below
}

// reconcileShape grows the recorded topology with the fleet before the
// components are rendered from it. Moving existing volumes onto a grown
// replication code is reconcileReplication's job, every pass, so a
// failed or interrupted move is never lost behind a shape that already
// reads as grown. Runs on managed installations only; the dev store is
// one process.
func (c *Controller) reconcileShape(ctx context.Context, row *store.ObjectStore) error {
	capable := len(c.deps.Observed.CapableNodes(layout.CapabilityObjectStorage))
	if capable == 0 {
		return nil
	}
	next, grew, below := growShape(*row, desiredShape(capable))
	if below {
		slog.Warn("substrate: object-storage fleet is below the recorded shape; shrinking is manual",
			"capableNodes", capable, "masters", row.Masters, "volumeServers", row.VolumeServers,
			"replication", row.Replication)
	}
	if !grew {
		return nil
	}
	if err := c.deps.DB.SetObjectStoreShape(ctx, row.ID, next); err != nil {
		return err
	}
	slog.Info("substrate: object store grown", "masters", next.Masters,
		"volumeServers", next.VolumeServers, "replication", next.Replication)
	row.Masters, row.VolumeServers, row.Replication = int32(next.Masters), int32(next.VolumeServers), next.Replication
	return nil
}

// reconcileReplication converges the live store onto the recorded
// replication code: the bucket path's filer.conf entry (new volumes) and
// every existing volume's placement (volume.configure.replication, which
// skips volumes already there; the master's maintenance loop then creates
// the missing copies). Observation-based like every substrate path: the
// recorded shape is the desired state, the filer document and the
// master's volume listing are the observed state, and nothing runs when
// they agree. A move that failed, stopped half way, or was interrupted by
// a restart between growing the shape and applying it is therefore
// simply retried on the next pass. Runs after the admin channel points at
// the filers and the masters answer; on the dev store there is nothing
// to move.
func (c *Controller) reconcileReplication(ctx context.Context, row store.ObjectStore) error {
	if c.deps.Seaweed == nil {
		return nil
	}
	changed, err := c.deps.Seaweed.EnsureReplicationPath(ctx, row.Replication)
	if err != nil {
		return fmt.Errorf("substrate: ensure bucket path replication: %w", err)
	}
	if changed {
		slog.Info("substrate: bucket path replication set", "replication", row.Replication)
	}
	health, err := c.deps.Seaweed.VolumeHealth(ctx, row.Replication)
	if err != nil {
		return fmt.Errorf("substrate: read volume replication: %w", err)
	}
	if health.Unconfigured == 0 {
		return nil
	}
	slog.Info("substrate: moving volumes to the recorded replication",
		"replication", row.Replication, "volumes", health.Unconfigured, "ids", health.UnconfiguredIDs)
	if err := c.deps.Seaweed.ConfigureVolumeReplication(ctx, row.Replication); err != nil {
		return fmt.Errorf("substrate: configure volume replication: %w", err)
	}
	return nil
}

// reconcileObjectStore drives the physical system: the metadata claim, the
// rendered components, the network fence, and readiness. Level-triggered
// like every substrate path; the REWORK 10.5 chain (metadata tenant ->
// filer -> s3 -> buckets) falls out of requeue-with-reason.
func (c *Controller) reconcileObjectStore(ctx context.Context) (time.Duration, error) {
	row, err := c.deps.DB.LiveObjectStore(ctx)
	if err != nil {
		if errors.Is(err, dbstore.ErrNotFound) {
			// The store comes up eagerly wherever the capability exists; no
			// capability, no work.
			if hasCapability(c.cfg.Capabilities, layout.CapabilityObjectStorage) {
				if row, err = c.ensureObjectStoreRow(ctx); err != nil {
					if wait, ok := errors.AsType[errWaiting](err); ok {
						slog.Info("substrate: object store pending", "reason", wait.reason)
						return requeueWait, nil
					}
					return 0, err
				}
			} else {
				return 0, nil
			}
		} else {
			return 0, err
		}
	}
	switch row.State {
	case dbstore.StateReleased:
		return 0, nil
	case dbstore.StateReleasing:
		return 0, c.releaseObjectStore(ctx, *row)
	}
	return c.ensureObjectStore(ctx, *row)
}

func (c *Controller) ensureObjectStore(ctx context.Context, row store.ObjectStore) (time.Duration, error) {
	if err := c.ensureNamespace(ctx); err != nil {
		return 0, err
	}

	// The metadata database is an ordinary system claim; until it
	// provisions, the store visibly waits (fresh bootstrap's first gate).
	outputs, err := c.EnsureSystemClaim(ctx, MetadataClaimKey, metadataClaimSpec())
	if err != nil {
		return 0, err
	}
	if !outputs.Provisioned {
		slog.Info("substrate: object store waiting for metadata database", "reason", outputs.Waiting)
		return requeueWait, nil
	}

	// The filer's store config is Secret-to-Secret: the login role and its
	// password are read from the claim's credential Secret and land only
	// in the filer store Secret.
	credential, err := c.deps.Cluster.GetSecret(ctx, Namespace, outputs.CredentialSecret)
	if err != nil {
		return 0, fmt.Errorf("substrate: read metadata credential: %w", err)
	}
	username := string(credential.Data[corev1.BasicAuthUsernameKey])
	password := string(credential.Data[corev1.BasicAuthPasswordKey])
	if username == "" || password == "" {
		return requeueWait, nil
	}
	storeSecret := seaweed.RenderFilerStoreSecret(Namespace,
		outputs.Host, outputs.Port, username, password, outputs.Database)
	if _, err := c.deps.Cluster.ApplyAs(ctx, storeSecret, kube.FieldManagerPlatform, false); err != nil {
		return 0, fmt.Errorf("substrate: ensure filer store secret: %w", err)
	}

	if c.cfg.Managed {
		if err := c.reconcileShape(ctx, &row); err != nil {
			return 0, err
		}
	}
	spec := seaweed.StoreSpec{
		Namespace:   Namespace,
		Masters:     int(row.Masters),
		Filers:      seaweed.FilersForNodes(c.fleetNodes()),
		Replication: row.Replication,
		Managed:     c.cfg.Managed,
		// The filer reads its store config only at start: hashing the
		// Secret's content into the pod template rolls the filers when the
		// metadata connection or credential changes.
		StoreConfigHash: secretDataHash(storeSecret),
	}
	objects := seaweed.RenderDev(spec)
	if c.cfg.Managed {
		objects = seaweed.RenderProduction(spec)
	} else {
		objects = append(objects, seaweed.RenderDevS3NodePort(Namespace))
	}
	objects = append(objects, seaweed.RenderFence(Namespace)...)
	if cidrs, err := c.deps.Cluster.ProxyCIDRs(ctx); err != nil {
		slog.Warn("substrate: derive proxy cidrs", "error", err)
	} else if len(cidrs) > 0 {
		objects = append(objects, seaweed.RenderAccessPolicy(Namespace, cidrs))
	}
	for _, obj := range objects {
		if _, err := c.deps.Cluster.ApplyAs(ctx, obj, kube.FieldManagerPlatform, false); err != nil {
			return 0, fmt.Errorf("substrate: ensure object store: %w", err)
		}
	}
	// The store pass is the backstop for the S3 access policy; claim
	// passes apply it the moment a holder appears or leaves.
	if err := c.ensureS3Access(ctx, row.ID); err != nil {
		return 0, err
	}
	if err := c.sweepLegacyS3Edge(ctx); err != nil {
		return 0, err
	}
	if err := c.sweepLegacyS3Open(ctx); err != nil {
		return 0, err
	}

	// Point the admin channel at the current filer pods.
	if c.deps.Seaweed != nil {
		if c.cfg.Managed {
			c.deps.Seaweed.SetFilerTarget("app="+seaweed.FilerService, "filer")
		} else {
			c.deps.Seaweed.SetFilerTarget("app="+seaweed.AllInOneApp, "seaweed")
		}
	}

	ready, reason, err := c.objectStoreReady(ctx, row)
	if err != nil {
		return 0, err
	}
	if !ready {
		slog.Debug("substrate: object store not ready", "reason", reason)
		return requeueWait, nil
	}
	// Keep the inert deny identity in the credential store forever so the
	// identity list can never go empty (empty = anonymous S3).
	if c.deps.Seaweed != nil {
		if err := c.deps.Seaweed.EnsureIdentity(ctx, seaweed.Identity{
			Name: seaweed.BootstrapIdentityName,
			Credentials: []seaweed.Credential{{
				AccessKey: seaweed.BootstrapAccessKey,
				SecretKey: seaweed.BootstrapSecretKey,
			}},
			Actions: []string{},
		}); err != nil {
			return 0, fmt.Errorf("substrate: ensure bootstrap identity: %w", err)
		}
		if err := c.ensurePlatformIdentity(ctx); err != nil {
			return 0, err
		}
	}
	// Last: the shell lock this takes can wait behind the master's
	// maintenance script, and a replication hiccup must never hold up the
	// identities bucket provisioning depends on.
	if c.cfg.Managed {
		if err := c.reconcileReplication(ctx, row); err != nil {
			return 0, err
		}
	}
	return 0, nil
}

// legacyS3EdgeRefs are the edge objects the installation-wide S3 endpoint
// owned before buckets published through their own routes (endpoints.s3,
// removed with issue #69): the TLS route, the redirecting plain-HTTP route,
// the redirect middleware, the certificate, and the plain Ingress a store
// published before the IngressRoute rework carried under the route's
// name. Nothing renders them any more; the list exists so an upgrade
// removes what an earlier release left behind. Drop it once no supported
// release can still carry the objects (first stable release).
func legacyS3EdgeRefs() []kube.ObjectRef {
	return []kube.ObjectRef{
		{GVK: edge.IngressRouteGVK, Namespace: Namespace, Name: "seaweed-s3"},
		{GVK: edge.IngressRouteGVK, Namespace: Namespace, Name: "seaweed-s3-http"},
		{GVK: edge.MiddlewareGVK, Namespace: Namespace, Name: edge.RedirectMiddlewareName},
		{GVK: edge.CertificateGVK, Namespace: Namespace, Name: "seaweed-s3-tls"},
		{GVK: schema.GroupVersionKind{Group: "networking.k8s.io", Version: "v1", Kind: "Ingress"}, Namespace: Namespace, Name: "seaweed-s3"},
	}
}

// sweepLegacyS3Edge deletes the objects of the removed installation-wide
// S3 endpoint once per process, on the first store pass. One sweep is
// enough: nothing can recreate the objects, and deleting by name every
// pass would only cost API calls. An unmanaged installation never rendered
// the edge, so it has nothing to sweep either: looking for the Certificate
// kind on a cluster without cert-manager would only reset the discovery
// cache. Bucket data is untouched: the refs name edge objects only.
func (c *Controller) sweepLegacyS3Edge(ctx context.Context) error {
	if !c.cfg.Managed || c.legacyEdgeSwept {
		return nil
	}
	removed, err := c.deleteRefs(ctx, legacyS3EdgeRefs())
	if err != nil {
		return err
	}
	c.legacyEdgeSwept = true
	if removed > 0 {
		slog.Info("substrate: legacy public S3 edge removed", "objects", removed)
	}
	return nil
}

// ensureS3Access applies the policy admitting the S3 port to the
// environments holding a bucket on the store (plus the platform's fixed
// peers). Serialised with the pool policies: see accessMu.
func (c *Controller) ensureS3Access(ctx context.Context, storeID uuid.UUID) error {
	c.accessMu.Lock()
	defer c.accessMu.Unlock()
	claims, err := c.deps.DB.ListStoreBucketClaims(ctx, storeID)
	if err != nil {
		return fmt.Errorf("substrate: list bucket holders: %w", err)
	}
	policy := seaweed.RenderS3AccessPolicy(Namespace, c.accessPeers(ctx, holderEnvironments(nil, claims)))
	if _, err := c.deps.Cluster.ApplyAs(ctx, policy, kube.FieldManagerPlatform, false); err != nil {
		return fmt.Errorf("substrate: ensure S3 access policy: %w", err)
	}
	return nil
}

// ensureS3AccessForLiveStore narrows or widens the S3 policy after a claim
// moved; without a live store there is no port to guard.
func (c *Controller) ensureS3AccessForLiveStore(ctx context.Context) error {
	sw, err := c.deps.DB.LiveObjectStore(ctx)
	if errors.Is(err, dbstore.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return c.ensureS3Access(ctx, sw.ID)
}

// sweepLegacyS3Open deletes, once per process and on every platform shape,
// the policy that opened the S3 port to every pod before claim holders
// were admitted by name (seaweed.LegacyS3OpenPolicy). Policies union, so
// an upgraded installation would otherwise keep the port open beside the
// new policy. Drop with the first stable release, like legacyS3EdgeRefs.
func (c *Controller) sweepLegacyS3Open(ctx context.Context) error {
	if c.legacyS3OpenSwept {
		return nil
	}
	removed, err := c.deleteRefs(ctx, []kube.ObjectRef{{
		GVK: schema.GroupVersionKind{Group: "networking.k8s.io", Version: "v1", Kind: "NetworkPolicy"}, Namespace: Namespace, Name: seaweed.LegacyS3OpenPolicy,
	}})
	if err != nil {
		return err
	}
	c.legacyS3OpenSwept = true
	if removed > 0 {
		slog.Info("substrate: legacy open S3 policy removed")
	}
	return nil
}

// deleteRefs deletes each ref, counting what actually went away. A kind
// the cluster does not serve (no cert-manager on a local platform, so no
// Certificate) counts as absent: the desired state is absence and there
// is nothing to remove.
func (c *Controller) deleteRefs(ctx context.Context, refs []kube.ObjectRef) (int, error) {
	removed := 0
	for _, ref := range refs {
		deleted, err := c.deps.Cluster.Delete(ctx, ref)
		if meta.IsNoMatchError(err) {
			continue
		}
		if err != nil {
			return removed, fmt.Errorf("substrate: delete %s: %w", ref, err)
		}
		if deleted {
			removed++
		}
	}
	return removed, nil
}

// PlatformCredentialSecret holds the platform identity's keypair: the
// admin principal the substrate itself speaks S3 as (bucket configuration
// today; restore writes and authenticated readiness follow). Generated
// once; never mirrored into an environment.
const PlatformCredentialSecret = "seaweed-platform"

// ensurePlatformIdentity creates the keypair Secret on first run, converges
// the Admin identity onto it, and hands the keypair to the admin client.
func (c *Controller) ensurePlatformIdentity(ctx context.Context) error {
	accessKey, secretKey, err := c.ensureKeypairSecret(ctx, PlatformCredentialSecret, map[string]string{
		seaweed.SystemLabel: "object-storage",
	})
	if err != nil {
		return err
	}
	if err := c.deps.Seaweed.EnsureIdentity(ctx, seaweed.Identity{
		Name:        seaweed.PlatformIdentityName,
		Credentials: []seaweed.Credential{{AccessKey: accessKey, SecretKey: secretKey}},
		Actions:     seaweed.PlatformActions(),
	}); err != nil {
		return fmt.Errorf("substrate: ensure platform identity: %w", err)
	}
	c.deps.Seaweed.SetPlatformCredentials(accessKey, secretKey)
	return nil
}

// ensureKeypairSecret returns the S3 keypair held in a platform Secret,
// generating it on first use. Secret keys exist only in Secrets; they are
// never logged or persisted elsewhere.
func (c *Controller) ensureKeypairSecret(ctx context.Context, name string, labels map[string]string) (string, string, error) {
	existing, err := c.deps.Cluster.GetSecret(ctx, Namespace, name)
	if err == nil {
		return string(existing.Data["access_key"]), string(existing.Data["secret_key"]), nil
	}
	if !apierrors.IsNotFound(err) {
		return "", "", fmt.Errorf("substrate: read %s: %w", name, err)
	}
	accessKey, secretKey := seaweed.GenerateAccessKey(), seaweed.GenerateSecretKey()
	secret := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: Namespace,
			Labels:    labels,
		},
		Type: corev1.SecretTypeOpaque,
		StringData: map[string]string{
			"access_key": accessKey,
			"secret_key": secretKey,
		},
	}
	if _, err := c.deps.Cluster.ApplyAs(ctx, secret, kube.FieldManagerPlatform, false); err != nil {
		return "", "", fmt.Errorf("substrate: apply %s: %w", name, err)
	}
	return accessKey, secretKey, nil
}

// objectStoreReady reads component rollout status at reconcile time (the
// poolReady pattern), with a human reason while it is not ready; continuous
// health is the provider observer's job.
func (c *Controller) objectStoreReady(ctx context.Context, row store.ObjectStore) (bool, string, error) {
	if c.cfg.Managed {
		masters, err := c.deps.Cluster.GetObject(ctx, statefulSetsGVR, Namespace, seaweed.MasterService)
		if err != nil {
			if apierrors.IsNotFound(err) {
				return false, "object store masters not created yet", nil
			}
			return false, "", err
		}
		if ready := workloadReadyCount(masters.Object); ready < int64(row.Masters) {
			return false, fmt.Sprintf("object store masters %d/%d ready", ready, row.Masters), nil
		}
	}
	name := seaweed.AllInOneApp
	if c.cfg.Managed {
		name = seaweed.FilerService
	}
	filer, err := c.deps.Cluster.GetObject(ctx, deploymentsGVR, Namespace, name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, "object store components not created yet", nil
		}
		return false, "", err
	}
	if workloadReadyCount(filer.Object) < 1 {
		return false, "object store rollout in progress", nil
	}
	return true, "", nil
}

// workloadReadyCount reads readyReplicas from a Deployment or StatefulSet
// status object.
func workloadReadyCount(object map[string]any) int64 {
	status, ok := object["status"].(map[string]any)
	if !ok {
		return 0
	}
	ready, _ := status["readyReplicas"].(int64)
	return ready
}

// releaseObjectStore tears the components down; volumes on owned hosts
// (production hostPath data) stay on disk by doctrine, the dev PVC is
// deleted with the store.
func (c *Controller) releaseObjectStore(ctx context.Context, row store.ObjectStore) error {
	refs := []kube.ObjectRef{
		{GVK: schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "StatefulSet"}, Namespace: Namespace, Name: seaweed.MasterService},
		{GVK: schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "DaemonSet"}, Namespace: Namespace, Name: seaweed.VolumeApp},
		{GVK: schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, Namespace: Namespace, Name: seaweed.FilerService},
		{GVK: schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, Namespace: Namespace, Name: seaweed.AllInOneApp},
		{GVK: schema.GroupVersionKind{Version: "v1", Kind: "Service"}, Namespace: Namespace, Name: seaweed.MasterService},
		{GVK: schema.GroupVersionKind{Version: "v1", Kind: "Service"}, Namespace: Namespace, Name: seaweed.FilerService},
		{GVK: schema.GroupVersionKind{Version: "v1", Kind: "Service"}, Namespace: Namespace, Name: seaweed.S3Service},
		{GVK: schema.GroupVersionKind{Version: "v1", Kind: "Service"}, Namespace: Namespace, Name: seaweed.S3ExternalService},
		{GVK: schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}, Namespace: Namespace, Name: "seaweed-master-config"},
		{GVK: schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}, Namespace: Namespace, Name: "seaweed-s3-bootstrap"},
		{GVK: schema.GroupVersionKind{Version: "v1", Kind: "Secret"}, Namespace: Namespace, Name: seaweed.FilerStoreSecret},
		{GVK: schema.GroupVersionKind{Version: "v1", Kind: "Secret"}, Namespace: Namespace, Name: PlatformCredentialSecret},
		{GVK: schema.GroupVersionKind{Version: "v1", Kind: "PersistentVolumeClaim"}, Namespace: Namespace, Name: "seaweed-data"},
		{GVK: schema.GroupVersionKind{Group: "policy", Version: "v1", Kind: "PodDisruptionBudget"}, Namespace: Namespace, Name: seaweed.MasterService},
		{GVK: schema.GroupVersionKind{Group: "policy", Version: "v1", Kind: "PodDisruptionBudget"}, Namespace: Namespace, Name: seaweed.FilerService},
		{GVK: schema.GroupVersionKind{Group: "networking.k8s.io", Version: "v1", Kind: "NetworkPolicy"}, Namespace: Namespace, Name: "seaweed-internal"},
		{GVK: schema.GroupVersionKind{Group: "networking.k8s.io", Version: "v1", Kind: "NetworkPolicy"}, Namespace: Namespace, Name: seaweed.S3AccessPolicy},
		{GVK: schema.GroupVersionKind{Group: "networking.k8s.io", Version: "v1", Kind: "NetworkPolicy"}, Namespace: Namespace, Name: seaweed.LegacyS3OpenPolicy},
		{GVK: schema.GroupVersionKind{Group: "networking.k8s.io", Version: "v1", Kind: "NetworkPolicy"}, Namespace: Namespace, Name: "seaweed-skalid-access"},
	}
	refs = append(refs, legacyS3EdgeRefs()...)
	if _, err := c.deleteRefs(ctx, refs); err != nil {
		return err
	}
	if err := c.ReleaseSystemClaim(ctx, MetadataClaimKey); err != nil && !errors.Is(err, dbstore.ErrNotFound) {
		return err
	}
	if _, err := c.deps.DB.TransitionObjectStore(ctx, row.ID, dbstore.StateReleased); err != nil {
		return err
	}
	slog.Info("substrate: object store released")
	return nil
}

// secretDataHash identifies a Secret's content (sorted key=value lines,
// sha256, 16 hex chars) without ever exposing it: only the hash lands on a
// pod template.
func secretDataHash(secret *corev1.Secret) string {
	lines := make([]string, 0, len(secret.StringData)+len(secret.Data))
	for key, value := range secret.StringData {
		lines = append(lines, key+"="+value)
	}
	for key, value := range secret.Data {
		lines = append(lines, key+"="+string(value))
	}
	slices.Sort(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:8])
}
