package substrate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/layout"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/substrate/seaweed"
)

// MetadataClaimKey is the object-storage subsystem's system database claim
// (REWORK_V2 10.3): the filer's metadata store is an ordinary internal
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
	}
	objects := seaweed.RenderDev(spec)
	if c.cfg.Managed {
		objects = seaweed.RenderProduction(spec)
	}
	if c.cfg.Managed && c.cfg.S3Domain != "" {
		objects = append(objects, seaweed.RenderS3Ingress(Namespace, c.cfg.S3Domain))
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
	}
	return 0, nil
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
		{GVK: schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}, Namespace: Namespace, Name: "seaweed-master-config"},
		{GVK: schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}, Namespace: Namespace, Name: "seaweed-s3-bootstrap"},
		{GVK: schema.GroupVersionKind{Version: "v1", Kind: "Secret"}, Namespace: Namespace, Name: seaweed.FilerStoreSecret},
		{GVK: schema.GroupVersionKind{Version: "v1", Kind: "PersistentVolumeClaim"}, Namespace: Namespace, Name: "seaweed-data"},
		{GVK: schema.GroupVersionKind{Group: "networking.k8s.io", Version: "v1", Kind: "Ingress"}, Namespace: Namespace, Name: "seaweed-s3"},
		{GVK: schema.GroupVersionKind{Group: "networking.k8s.io", Version: "v1", Kind: "NetworkPolicy"}, Namespace: Namespace, Name: "seaweed-internal"},
		{GVK: schema.GroupVersionKind{Group: "networking.k8s.io", Version: "v1", Kind: "NetworkPolicy"}, Namespace: Namespace, Name: "seaweed-s3-open"},
		{GVK: schema.GroupVersionKind{Group: "networking.k8s.io", Version: "v1", Kind: "NetworkPolicy"}, Namespace: Namespace, Name: "seaweed-skalid-access"},
	}
	for _, ref := range refs {
		if _, err := c.deps.Cluster.Delete(ctx, ref); err != nil {
			return fmt.Errorf("substrate: delete %s: %w", ref, err)
		}
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
