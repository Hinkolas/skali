package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/revision"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/substrate"
	"github.com/Hinkolas/skali/internal/utils"
)

// maxManifestBytes bounds a snapshot manifest read; a manifest carries one
// revision document plus component metadata and stays far below this.
const maxManifestBytes = 8 << 20

// errCancelled aborts execution when the run was cancelled from outside.
var errCancelled = errors.New("backup: cancelled")

// process executes one backup row end to end. The row's from-status guard
// makes the claim exclusive: a second worker or a recovered row is a 0-row
// update and a silent no-op.
func (c *Controller) process(ctx context.Context, id uuid.UUID) error {
	row, err := c.deps.Store.GetBackup(ctx, id)
	if err != nil {
		return fmt.Errorf("backup: get row: %w", err)
	}
	if row.Status != StatusPending {
		return nil
	}
	claimed, err := c.deps.Store.SetBackupStatus(ctx, store.SetBackupStatusParams{
		ID: row.ID, ToStatus: StatusRunning, FromStatus: StatusPending,
	})
	if err != nil {
		return fmt.Errorf("backup: claim row: %w", err)
	}
	if claimed == 0 {
		return nil
	}
	if row.RunID == nil {
		return c.failRow(ctx, row.ID, "backup row carries no run")
	}

	redactor, err := c.deps.Values.Redactor(ctx, row.EnvironmentID, uuid.Nil)
	if err != nil {
		return c.failRow(ctx, row.ID, fmt.Sprintf("build redactor: %v", err))
	}
	scope := journal.NewScope(c.deps.Journal, redactor, *row.RunID)

	switch row.Kind {
	case KindBackup:
		err = c.executeBackup(ctx, scope, &row)
	case KindRestore:
		err = c.executeRestore(ctx, scope, &row)
	default:
		err = fmt.Errorf("backup: unknown row kind %q", row.Kind)
	}
	if err != nil {
		// The run's failure reason and the row's error are the same text by
		// construction: whoever reads either sees the same summary.
		status, message := journal.RunFailed, err.Error()
		if errors.Is(err, errCancelled) {
			status, message = journal.RunCancelled, "cancelled"
		}
		scope.Finish(ctx, status, message)
		c.cleanupJobs(ctx, &row)
		return c.failRow(ctx, row.ID, message)
	}
	scope.Finish(ctx, journal.RunSucceeded, "")
	c.cleanupJobs(ctx, &row)
	if _, err := c.deps.Store.SetBackupStatus(ctx, store.SetBackupStatusParams{
		ID: row.ID, ToStatus: StatusSucceeded, FromStatus: StatusRunning,
	}); err != nil {
		return fmt.Errorf("backup: finish row: %w", err)
	}
	return nil
}

func (c *Controller) failRow(ctx context.Context, id uuid.UUID, message string) error {
	if _, err := c.deps.Store.SetBackupStatus(ctx, store.SetBackupStatusParams{
		ID: id, ToStatus: StatusFailed, FromStatus: StatusRunning, Error: &message,
	}); err != nil {
		return fmt.Errorf("backup: fail row: %w", err)
	}
	return nil
}

// backupContext carries one operation's resolved collaborators between
// component executors.
type backupContext struct {
	row         *store.Backup
	credentials *Credentials
	target      objectStore
	snapshotID  string
	// workerImage is resolved lazily, only when a Job-backed component
	// (database or volume) actually runs.
	workerImage string
}

func (b *backupContext) prefix() string { return b.credentials.Prefix }

func (c *Controller) workerImageFor(ctx context.Context, bctx *backupContext) (string, error) {
	if bctx.workerImage != "" {
		return bctx.workerImage, nil
	}
	image, err := c.resolveWorkerImage(ctx)
	if err != nil {
		return "", err
	}
	bctx.workerImage = image
	return image, nil
}

// backupNamespace retains pre-rename paths, including rows accepted by an
// older daemon during rollout overlap.
func backupNamespace(row *store.Backup) string {
	if row.EnvironmentNamespace != "" {
		return row.EnvironmentNamespace
	}
	return row.EnvironmentName
}

// executeBackup drives one snapshot: target check, every component in
// deterministic order, then the manifest write that certifies completion.
func (c *Controller) executeBackup(ctx context.Context, scope *journal.Scope, row *store.Backup) error {
	credentials, err := c.deps.Targets.credentials(ctx, DefaultTargetName)
	if err != nil {
		return err
	}
	bctx := &backupContext{row: row, credentials: credentials, snapshotID: row.ID.String()}
	if err := scope.Step(ctx, "target", "Check backup target", func(ctx context.Context, log *journal.StepLog) error {
		bctx.target, err = c.openStore(targetLocation(credentials))
		if err != nil {
			return err
		}
		if err := bctx.target.Reachable(ctx); err != nil {
			return err
		}
		log.Info(ctx, fmt.Sprintf("writing to %s/%s", credentials.Endpoint, credentials.Bucket))
		return nil
	}); err != nil {
		return err
	}

	// Planning runs under its own step so a revision that cannot be loaded
	// or holds nothing to snapshot fails with the reason in the run, not in
	// a column nothing reads. CreateBackup already refused the empty case;
	// this is the executor's own account of what the snapshot holds.
	snapshotID := bctx.snapshotID
	var (
		revisionDoc *revision.Revision
		components  []Component
	)
	if err := scope.Step(ctx, "plan", "Plan snapshot contents", func(ctx context.Context, log *journal.StepLog) error {
		if row.RevisionID == nil {
			return errors.New("row carries no revision")
		}
		doc, err := c.revisions.GetRevision(ctx, *row.RevisionID)
		if err != nil {
			return fmt.Errorf("load revision: %w", err)
		}
		revisionDoc = doc
		components = planComponents(&doc.Definition)
		if len(components) == 0 {
			return ErrNothingToBackUp
		}
		for _, component := range components {
			log.Info(ctx, "snapshot holds "+componentLabel(component))
		}
		return nil
	}); err != nil {
		return err
	}

	for i := range components {
		if scope.Cancelled(ctx) {
			return errCancelled
		}
		component := &components[i]
		if err := c.backupComponent(ctx, scope, bctx, component); err != nil {
			return err
		}
		component.Status = "complete"
	}

	manifest := &Manifest{
		FormatVersion:    ManifestFormatVersion,
		SnapshotID:       snapshotID,
		Encryption:       EncryptionNone,
		SkaliVersion:     c.deps.Version,
		Project:          row.ProjectName,
		Environment:      row.EnvironmentName,
		EnvironmentID:    row.EnvironmentID.String(),
		RevisionChecksum: revisionDoc.Checksum,
		Trigger:          row.Trigger,
		Strategy:         row.Strategy,
		Components:       components,
	}
	if err := scope.Step(ctx, "manifest", "Write snapshot manifest", func(ctx context.Context, log *journal.StepLog) error {
		revisionJSON, err := json.Marshal(revisionDoc)
		if err != nil {
			return fmt.Errorf("encode revision: %w", err)
		}
		manifest.CreatedAt = time.Now().UTC()
		manifest.Revision = revisionJSON
		data, err := encodeManifest(manifest)
		if err != nil {
			return err
		}
		key := manifestKey(credentials.Prefix, row.ProjectName, backupNamespace(row), snapshotID)
		if err := bctx.target.Put(ctx, key, bytes.NewReader(data), int64(len(data))); err != nil {
			return err
		}
		// Read back before certifying: a manifest that cannot be listed is
		// a snapshot that does not exist.
		if _, err := bctx.target.Stat(ctx, key); err != nil {
			return fmt.Errorf("verify manifest: %w", err)
		}
		if err := c.deps.Store.SetBackupSnapshotKey(ctx, store.SetBackupSnapshotKeyParams{
			ID: row.ID, SnapshotKey: key,
		}); err != nil {
			return fmt.Errorf("record snapshot key: %w", err)
		}
		log.Info(ctx, "snapshot "+snapshotID+" complete")
		return nil
	}); err != nil {
		return err
	}

	if row.Trigger != TriggerScheduled {
		return nil
	}
	// The snapshot is complete and listed; what follows only removes older
	// scheduled snapshots of this environment. The retention is the one in
	// force when the run was accepted. A failure here fails the run so it
	// is seen, and the next scheduled run sweeps again.
	retention := time.Duration(row.RetentionSeconds) * time.Second
	return scope.Step(ctx, "retention", "Apply retention", func(ctx context.Context, log *journal.StepLog) error {
		return c.applyRetention(ctx, log, bctx, retention, manifest.CreatedAt)
	})
}

// planComponents enumerates the stateful components a snapshot holds, in
// deterministic order: databases, buckets, then application volumes.
func planComponents(definition *compiler.ProjectDefinition) []Component {
	var components []Component
	for _, key := range utils.SortedKeys(definition.Databases) {
		components = append(components, Component{Kind: ComponentDatabase, ServiceKey: key})
	}
	for _, key := range utils.SortedKeys(definition.Buckets) {
		components = append(components, Component{Kind: ComponentBucket, ServiceKey: key})
	}
	for _, appKey := range utils.SortedKeys(definition.Applications) {
		for _, volume := range utils.SortedKeys(definition.Applications[appKey].Volumes) {
			components = append(components, Component{
				Kind: ComponentVolume, Application: appKey, Volume: volume,
			})
		}
	}
	return components
}

func (c *Controller) backupComponent(ctx context.Context, scope *journal.Scope, bctx *backupContext, component *Component) error {
	label := componentLabel(*component)
	switch component.Kind {
	case ComponentBucket:
		title := "Back up bucket " + component.ServiceKey
		return scope.Step(ctx, label, title, func(ctx context.Context, log *journal.StepLog) error {
			return c.backupBucket(ctx, log, bctx, component)
		})
	case ComponentDatabase:
		title := "Back up database " + component.ServiceKey
		return scope.Step(ctx, label, title, func(ctx context.Context, log *journal.StepLog) error {
			return c.backupDatabase(ctx, log, bctx, component)
		})
	default:
		title := fmt.Sprintf("Back up volume %s of %s", component.Volume, component.Application)
		return scope.Step(ctx, label, title, func(ctx context.Context, log *journal.StepLog) error {
			return c.backupVolume(ctx, log, bctx, component)
		})
	}
}

// backupBucket copies every object of the service's bucket, metadata
// included, into the target under the snapshot's bucket prefix. The read
// side is the platform identity: a bucket that is full (read-only for its
// own identity) or fenced still backs up.
func (c *Controller) backupBucket(ctx context.Context, log *journal.StepLog, bctx *backupContext, component *Component) error {
	row, target := bctx.row, bctx.target
	source, bucketName, err := c.openBucket(ctx, row.EnvironmentID, component.ServiceKey)
	if err != nil {
		return err
	}
	log.Info(ctx, "counting objects in bucket "+bucketName)
	// Keep the metadata pre-pass for real totals, but replay its inventory
	// during the copy instead of walking the slow source listing twice.
	inventory, total, err := inventoryBucket(ctx, log, source)
	if err != nil {
		return err
	}
	defer inventory.Close()
	log.Info(ctx, fmt.Sprintf("copying %d objects from bucket %s", total, bucketName))
	log.Progress(ctx, 0, total)

	destinationPrefix := bucketPrefixKey(bctx.prefix(), row.ProjectName, backupNamespace(row),
		component.ServiceKey, bctx.snapshotID)
	opts := c.copyOptions()
	opts.SkipMissing = true
	copied, copiedBytes, err := copyObjects(ctx, log, inventory, target, "", destinationPrefix, total, opts)
	if err != nil {
		return err
	}
	log.Info(ctx, fmt.Sprintf("copied %d objects (%d bytes)", copied, copiedBytes))
	component.ObjectPrefix = destinationPrefix
	component.ObjectCount = copied
	component.Bytes = copiedBytes
	return nil
}

// openServiceBucket opens the service's live bucket on the in-cluster
// gateway as the platform identity (never the bucket's own credentials,
// which a restore deletes).
func (c *Controller) openServiceBucket(ctx context.Context, environmentID uuid.UUID, serviceKey string) (objectStore, string, error) {
	if c.deps.Buckets == nil {
		return nil, "", errors.New("the object-storage substrate is not available")
	}
	access, err := c.deps.Buckets.PlatformBucketAccess(ctx, environmentID, serviceKey)
	if err != nil {
		return nil, "", err
	}
	source, err := newObjectStore(s3Location{
		Endpoint:  access.Endpoint,
		Region:    access.Region,
		Bucket:    access.Bucket,
		AccessKey: access.AccessKey,
		SecretKey: access.SecretKey,
	})
	if err != nil {
		return nil, "", err
	}
	return source, access.Bucket, nil
}

// backupDatabase dumps one database service through a Job in the platform
// namespace: the pool's own pinned postgres image runs pg_dump into an
// emptyDir, the worker container uploads the dump with its sha256. Bulk
// bytes never transit the daemon.
func (c *Controller) backupDatabase(ctx context.Context, log *journal.StepLog, bctx *backupContext, component *Component) error {
	row := bctx.row
	identity, err := c.databaseIdentity(ctx, row.EnvironmentID, component.ServiceKey)
	if err != nil {
		return err
	}
	workerImage, err := c.workerImageFor(ctx, bctx)
	if err != nil {
		return err
	}
	if err := c.ensureTargetSecret(ctx, substrate.Namespace, bctx.credentials); err != nil {
		return err
	}
	key := databaseKey(bctx.prefix(), row.ProjectName, backupNamespace(row),
		component.ServiceKey, bctx.snapshotID)
	identity.WorkerImage = workerImage
	identity.TargetSecret = targetSecretName
	identity.SnapshotObject = key
	name := jobName("skali-backup", row.ID.String(), "db", component.ServiceKey)
	log.Info(ctx, "dumping database "+identity.DatabaseName)
	if err := c.runJob(ctx, log, renderDatabaseBackupJob(name, substrate.Namespace, row.ID.String(), identity)); err != nil {
		return err
	}
	stat, err := bctx.target.Stat(ctx, key)
	if err != nil {
		return fmt.Errorf("verify dump upload: %w", err)
	}
	component.ObjectKey = key
	component.Bytes = stat.Size
	component.SHA256 = stat.SHA256
	return nil
}

// databaseIdentity resolves a database service's tenant connection facts
// and the pool's pinned image (which carries the matching pg_dump).
func (c *Controller) databaseIdentity(ctx context.Context, environmentID uuid.UUID, serviceKey string) (databaseJobIdentity, error) {
	claim, err := c.deps.DB.LiveServiceClaim(ctx, environmentID, serviceKey)
	if err != nil {
		return databaseJobIdentity{}, fmt.Errorf("resolve database claim for %s: %w", serviceKey, err)
	}
	tenant, err := c.deps.DB.LiveTenant(ctx, claim.ID)
	if err != nil {
		return databaseJobIdentity{}, fmt.Errorf("resolve database tenant for %s: %w", serviceKey, err)
	}
	cluster, err := c.deps.DB.GetCluster(ctx, tenant.ClusterID)
	if err != nil {
		return databaseJobIdentity{}, fmt.Errorf("resolve database cluster for %s: %w", serviceKey, err)
	}
	return databaseJobIdentity{
		Host:          tenant.Host,
		Port:          tenant.Port,
		DatabaseName:  tenant.DatabaseName,
		SecretName:    tenant.CredentialSecret,
		PostgresImage: cluster.Image,
	}, nil
}

// backupVolume archives one application volume through a Job in the
// environment namespace. The PVC mount is read-only and the bound PV's
// node affinity co-schedules the Job with the data.
func (c *Controller) backupVolume(ctx context.Context, log *journal.StepLog, bctx *backupContext, component *Component) error {
	row := bctx.row
	workerImage, err := c.workerImageFor(ctx, bctx)
	if err != nil {
		return err
	}
	namespace := kubernetes.NamespaceName(row.EnvironmentID.String())
	if err := c.ensureTargetSecret(ctx, namespace, bctx.credentials); err != nil {
		return err
	}
	claimName := kubernetes.VolumeClaimName(row.ProjectName, component.Application, component.Volume)
	key := volumeKey(bctx.prefix(), row.ProjectName, backupNamespace(row),
		component.Application, component.Volume, bctx.snapshotID)
	name := jobName("skali-backup", row.ID.String(), "vol", component.Application, component.Volume)
	log.Info(ctx, "archiving volume claim "+claimName)
	job := renderVolumeJob(name, namespace, row.ID.String(), workerImage, targetSecretName, claimName, key, false)
	if err := c.runJob(ctx, log, job); err != nil {
		return err
	}
	stat, err := bctx.target.Stat(ctx, key)
	if err != nil {
		return fmt.Errorf("verify volume upload: %w", err)
	}
	component.ObjectKey = key
	component.Bytes = stat.Size
	return nil
}

// cleanupJobs removes every Job and per-operation Secret the operation
// created, in both namespaces it may have touched.
func (c *Controller) cleanupJobs(ctx context.Context, row *store.Backup) {
	if c.deps.Kube == nil {
		return
	}
	namespaces := []string{substrate.Namespace,
		kubernetes.NamespaceName(row.EnvironmentID.String())}
	for _, namespace := range namespaces {
		jobs, err := c.deps.Kube.Clientset.BatchV1().Jobs(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: backupJobLabel + "=" + row.ID.String(),
		})
		if err == nil {
			for _, job := range jobs.Items {
				c.deleteJob(ctx, namespace, job.Name)
			}
		}
		c.deleteTargetSecret(ctx, namespace)
	}
}

func readAll(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("object exceeds %d bytes", limit)
	}
	return data, nil
}
