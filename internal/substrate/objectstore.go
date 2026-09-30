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
		input.Masters = seaweed.MastersForNodes(capable)
		input.VolumeServers = capable
		input.Replication = seaweed.ReplicationForNodes(capable)
		// Production volume servers use the node's disk directly; the
		// desired-shape row records no PVC size.
		input.VolumeStorageBytes = 0
	}
	row, err = c.deps.DB.CreateObjectStore(ctx, input)
	if err != nil {
		return nil, err
	}
	slog.Info("substrate: object store created", "masters", input.Masters,
		"volumeServers", input.VolumeServers, "replication", input.Replication)
	return row, nil
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

	// The filer's store config is Secret-to-Secret: the password is read
	// from the claim's credential Secret and lands only in the filer store
	// Secret.
	credential, err := c.deps.Cluster.GetSecret(ctx, Namespace, outputs.CredentialSecret)
	if err != nil {
		return 0, fmt.Errorf("substrate: read metadata credential: %w", err)
	}
	password := string(credential.Data[corev1.BasicAuthPasswordKey])
	if password == "" {
		return requeueWait, nil
	}
	storeSecret := seaweed.RenderFilerStoreSecret(Namespace,
		outputs.Host, outputs.Port, outputs.Username, password, outputs.Database)
	if _, err := c.deps.Cluster.ApplyAs(ctx, storeSecret, kube.FieldManagerPlatform, false); err != nil {
		return 0, fmt.Errorf("substrate: ensure filer store secret: %w", err)
	}

	spec := seaweed.StoreSpec{
		Namespace:   Namespace,
		Masters:     int(row.Masters),
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
	if err := c.reconcilePublicEdge(ctx); err != nil {
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
	return 0, nil
}

// publicEdgeEnabled reports whether the store publishes through the edge:
// a managed installation with an S3 domain. Everything else keeps bucket
// access in-cluster and owns no edge objects.
func (c *Controller) publicEdgeEnabled() bool {
	return c.cfg.Managed && c.cfg.S3Domain != ""
}

// s3EdgeRefs are the edge objects the public S3 endpoint owns: the TLS
// route, the redirecting plain-HTTP route, the redirect middleware, and
// the certificate. Stores published before the IngressRoute rework carried
// a plain Ingress under the route's name; it stays in the list so an
// upgrade removes it too.
func s3EdgeRefs() []kube.ObjectRef {
	return []kube.ObjectRef{
		{GVK: edge.IngressRouteGVK, Namespace: Namespace, Name: "seaweed-s3"},
		{GVK: edge.IngressRouteGVK, Namespace: Namespace, Name: "seaweed-s3-http"},
		{GVK: edge.MiddlewareGVK, Namespace: Namespace, Name: edge.RedirectMiddlewareName},
		{GVK: edge.CertificateGVK, Namespace: Namespace, Name: "seaweed-s3-tls"},
		{GVK: schema.GroupVersionKind{Group: "networking.k8s.io", Version: "v1", Kind: "Ingress"}, Namespace: Namespace, Name: "seaweed-s3"},
	}
}

// reconcilePublicEdge is level-triggered like the rest of the store:
// while the endpoint is configured the edge objects are applied under
// their fixed names, so a domain change replaces the host match and the
// certificate's name in place and nothing of the old domain lingers; once
// endpoints.s3 is cleared they are deleted every pass, so disabling the
// endpoint stops the domain from answering instead of leaving the routes
// and the certificate behind. An unmanaged installation never renders the
// edge (there is no edge to serve it), so it has nothing to sweep either:
// looking for the Certificate kind on a cluster without cert-manager would
// only reset the discovery cache each pass. Bucket data is untouched
// either way: the refs name edge objects only.
func (c *Controller) reconcilePublicEdge(ctx context.Context) error {
	if c.publicEdgeEnabled() {
		for _, obj := range seaweed.RenderS3Edge(Namespace, c.cfg.S3Domain) {
			if _, err := c.deps.Cluster.ApplyAs(ctx, obj, kube.FieldManagerPlatform, false); err != nil {
				return fmt.Errorf("substrate: ensure public S3 edge: %w", err)
			}
		}
		return nil
	}
	if !c.cfg.Managed {
		return nil
	}
	removed, err := c.deleteRefs(ctx, s3EdgeRefs())
	if err != nil {
		return err
	}
	if removed > 0 {
		slog.Info("substrate: public S3 edge removed", "objects", removed)
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
		{GVK: schema.GroupVersionKind{Group: "networking.k8s.io", Version: "v1", Kind: "NetworkPolicy"}, Namespace: Namespace, Name: "seaweed-internal"},
		{GVK: schema.GroupVersionKind{Group: "networking.k8s.io", Version: "v1", Kind: "NetworkPolicy"}, Namespace: Namespace, Name: "seaweed-s3-open"},
		{GVK: schema.GroupVersionKind{Group: "networking.k8s.io", Version: "v1", Kind: "NetworkPolicy"}, Namespace: Namespace, Name: "seaweed-skalid-access"},
	}
	refs = append(refs, s3EdgeRefs()...)
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
