package backup

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/deploy"
	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/substrate"
	"github.com/Hinkolas/skali/internal/utils"
)

// ErrSnapshotNotFound: the named snapshot has no manifest on the target.
var ErrSnapshotNotFound = errors.New("backup: snapshot not found")

const (
	stopPollInterval       = 2 * time.Second
	reconvergePollInterval = 3 * time.Second
)

// RestoreInput names what to restore and who asked. SourceAllowed, when set,
// is consulted with the name of the environment holding the snapshot before
// any data is read; an error it returns aborts the restore untouched (the API
// uses it for the read-on-source access check).
type RestoreInput struct {
	EnvironmentID uuid.UUID
	SnapshotID    string
	Actor         string
	SourceAllowed func(environment string) error
}

// CreateRestore accepts a stop-first restore of one of the project's
// snapshots into the environment. The environment must be active with a
// target revision, or held by an incomplete restore: the restore stops it,
// moves data, and resumes exactly that revision, or the one the incomplete
// restore was to resume. The snapshot manifest is verified to exist before
// anything is accepted.
func (c *Controller) CreateRestore(ctx context.Context, in RestoreInput) (*CreateResult, error) {
	environmentID, snapshotID, actor := in.EnvironmentID, in.SnapshotID, in.Actor
	names, err := c.environmentNames(ctx, environmentID)
	if err != nil {
		return nil, err
	}
	resumeRevision, err := c.resumeRevision(ctx, environmentID)
	if err != nil {
		return nil, err
	}
	revisionDoc, err := c.revisions.GetRevision(ctx, resumeRevision)
	if err != nil {
		return nil, fmt.Errorf("backup: load revision: %w", err)
	}
	if err := c.bucketsReady(ctx, &revisionDoc.Definition); err != nil {
		return nil, err
	}

	// The snapshot may come from any environment of the project: a
	// production snapshot restores into staging just as well, since
	// components match by service key and the manifest carries absolute
	// object keys.
	credentials, targetStore, err := c.openTarget(ctx)
	if err != nil {
		return nil, err
	}
	manifestObjectKey, sourceEnvironment, err := c.findSnapshot(ctx, targetStore, credentials.Prefix, names.project, snapshotID)
	if err != nil {
		return nil, err
	}
	if in.SourceAllowed != nil {
		if err := in.SourceAllowed(sourceEnvironment); err != nil {
			return nil, err
		}
	}

	// Everything that can fail before the row exists runs before the run
	// begins: a run without its row holds the environment's run slot.
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("backup: generate id: %w", err)
	}
	run, err := c.deps.Journal.BeginRun(ctx, journal.RunInput{
		Kind:          KindRestore,
		ProjectID:     names.projectID,
		EnvironmentID: environmentID,
		Actor:         actor,
	})
	if errors.Is(err, journal.ErrRunConflict) {
		return nil, ErrBackupInFlight
	}
	if err != nil {
		return nil, fmt.Errorf("backup: begin run: %w", err)
	}

	runID := run.ID
	row, err := c.deps.Store.CreateBackup(ctx, store.CreateBackupParams{
		ID:              id,
		Kind:            KindRestore,
		EnvironmentID:   environmentID,
		ProjectName:     names.project,
		EnvironmentName: names.environment,
		// The revision the environment resumes after data movement.
		RevisionID: &resumeRevision,
		RunID:      &runID,
		Trigger:    TriggerManual,
		Strategy:   StrategyComplete,
	})
	if err != nil {
		c.abandon(ctx, run.ID, nil, "recording the restore failed: "+err.Error())
		return nil, fmt.Errorf("backup: create row: %w", err)
	}
	if err := c.deps.Store.SetBackupSnapshotKey(ctx, store.SetBackupSnapshotKeyParams{
		ID: row.ID, SnapshotKey: manifestObjectKey,
	}); err != nil {
		c.abandon(ctx, run.ID, &row.ID, "recording the snapshot key failed: "+err.Error())
		return nil, fmt.Errorf("backup: record snapshot key: %w", err)
	}
	c.Enqueue(row.ID)
	return &CreateResult{BackupID: row.ID, RunID: run.ID}, nil
}

// resumeRevision is the revision a restore accepted now resumes: the
// target of an active environment, or, for an environment an incomplete
// restore holds, the revision that restore was to resume. A restore re-run
// over a failed one is how the environment comes back on complete data.
func (c *Controller) resumeRevision(ctx context.Context, environmentID uuid.UUID) (uuid.UUID, error) {
	target, err := c.deps.Store.GetEnvironmentTarget(ctx, environmentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, ErrEnvironmentNotFound
		}
		return uuid.Nil, fmt.Errorf("backup: get environment target: %w", err)
	}
	switch {
	case target.State == deploy.EnvironmentStateReleasing:
		return uuid.Nil, ErrEnvironmentNotActive
	case target.IncompleteRestoreID != nil:
		held, err := c.deps.Store.GetBackup(ctx, *target.IncompleteRestoreID)
		if err != nil {
			return uuid.Nil, fmt.Errorf("backup: get the incomplete restore: %w", err)
		}
		if held.RevisionID == nil {
			return uuid.Nil, errors.New("backup: the incomplete restore carries no revision")
		}
		return *held.RevisionID, nil
	case target.State != deploy.EnvironmentStateActive || target.TargetRevisionID == nil:
		return uuid.Nil, ErrEnvironmentNotActive
	}
	return *target.TargetRevisionID, nil
}

// executeRestore drives one stop-first restore: load the manifest, verify
// the snapshot's data, stop the environment's workloads, restore every
// matched component, resume the captured revision, and wait for
// reconvergence. Nothing destructive happens before the snapshot is
// verified. From the stop on, the environment is held: on failure it
// deliberately stays down, and no deployment brings it back, because
// resuming applications over half-restored data is worse than visible
// downtime. Re-running the restore is the way forward, or accepting the
// data as it is.
func (c *Controller) executeRestore(ctx context.Context, scope *journal.Scope, row *store.Backup) error {
	credentials, err := c.deps.Targets.credentials(ctx, DefaultTargetName)
	if err != nil {
		return err
	}
	bctx := &backupContext{row: row, credentials: credentials}
	if err := scope.Step(ctx, "target", "Check backup target", func(ctx context.Context, log *journal.StepLog) error {
		bctx.target, err = c.openStore(targetLocation(credentials))
		if err != nil {
			return err
		}
		if err := bctx.target.Reachable(ctx); err != nil {
			return err
		}
		log.Info(ctx, fmt.Sprintf("reading from %s/%s", credentials.Endpoint, credentials.Bucket))
		return nil
	}); err != nil {
		return err
	}

	var manifest *Manifest
	if err := scope.Step(ctx, "snapshot", "Load snapshot manifest", func(ctx context.Context, log *journal.StepLog) error {
		manifest, err = c.readManifest(ctx, bctx.target, row.SnapshotKey)
		if err != nil {
			return err
		}
		if err := validateManifest(manifest, row.SnapshotKey); err != nil {
			return err
		}
		bctx.snapshotID = manifest.SnapshotID
		log.Info(ctx, fmt.Sprintf("snapshot %s of %s/%s, taken %s",
			manifest.SnapshotID, manifest.Project, manifest.Environment,
			manifest.CreatedAt.UTC().Format(time.RFC3339)))
		return nil
	}); err != nil {
		return err
	}

	if row.RevisionID == nil {
		return errors.New("backup: restore row carries no revision")
	}
	revisionDoc, err := c.revisions.GetRevision(ctx, *row.RevisionID)
	if err != nil {
		return fmt.Errorf("backup: load revision: %w", err)
	}

	restored := restorableComponents(&revisionDoc.Definition, manifest.Components)
	if err := scope.Step(ctx, "verify", "Verify snapshot data", func(ctx context.Context, log *journal.StepLog) error {
		if err := c.verifySnapshot(ctx, log, bctx, manifest, restored); err != nil {
			return fmt.Errorf("%w; nothing was changed", err)
		}
		return nil
	}); err != nil {
		return err
	}

	// The last moment a cancel leaves the environment as it was: from here
	// on, a restore that stops halfway keeps it down.
	if ctx.Err() != nil || scope.Cancelled(ctx) {
		return errCancelled
	}
	if err := scope.Step(ctx, "stop", "Stop environment workloads", func(ctx context.Context, log *journal.StepLog) error {
		return c.stopEnvironment(ctx, log, row, restored)
	}); err != nil {
		return err
	}

	for _, component := range manifest.Components {
		if scope.Cancelled(ctx) {
			return errCancelled
		}
		if err := c.restoreComponent(ctx, scope, bctx, &revisionDoc.Definition, component); err != nil {
			return err
		}
	}

	if err := scope.Step(ctx, "start", "Resume environment", func(ctx context.Context, log *journal.StepLog) error {
		if row.RevisionID == nil {
			return errors.New("restore has no resume revision")
		}
		if err := c.resume(ctx, row.EnvironmentID, *row.RevisionID, row.ID); err != nil {
			return err
		}

		if c.deps.Enqueue != nil {
			c.deps.Enqueue(row.EnvironmentID)
		}
		log.Info(ctx, "target restored to revision "+utils.ShortChecksum(revisionDoc.Checksum))
		return nil
	}); err != nil {
		return err
	}

	return scope.Step(ctx, "reconverge", "Wait for environment health", func(ctx context.Context, log *journal.StepLog) error {
		return c.awaitReconverge(ctx, log, row)
	})
}

// restorableComponents is the part of a snapshot a restore rewrites: the
// components whose service the resumed revision still declares. The rest
// are skipped visibly when the restore reaches them.
func restorableComponents(definition *compiler.ProjectDefinition, components []Component) []Component {
	var restorable []Component
	for _, component := range components {
		if declaresComponent(definition, component) {
			restorable = append(restorable, component)
		}
	}
	return restorable
}

func declaresComponent(definition *compiler.ProjectDefinition, component Component) bool {
	switch component.Kind {
	case ComponentDatabase:
		_, exists := definition.Databases[component.ServiceKey]
		return exists
	case ComponentBucket:
		_, exists := definition.Buckets[component.ServiceKey]
		return exists
	case ComponentVolume:
		application, exists := definition.Applications[component.Application]
		if exists {
			_, exists = application.Volumes[component.Volume]
		}
		return exists
	}
	return false
}

// verifySnapshot proves, before anything is stopped or overwritten, that
// every component the restore rewrites is stored as its manifest records,
// and that the restore covers whatever an incomplete restore holding the
// environment had set out to rewrite: lifting that hold on a restore that
// leaves one of those components half-written would bring the environment
// back on partial data after all.
func (c *Controller) verifySnapshot(ctx context.Context, log *journal.StepLog, bctx *backupContext,
	manifest *Manifest, restored []Component) error {
	labels := make(map[string]bool, len(restored))
	for _, component := range restored {
		labels[componentLabel(component)] = true
	}
	target, err := c.deps.Store.GetEnvironmentTarget(ctx, bctx.row.EnvironmentID)
	if err != nil {
		return fmt.Errorf("read environment target: %w", err)
	}
	if held := target.IncompleteRestoreID; held != nil && *held != bctx.row.ID {
		var uncovered []string
		for _, label := range target.IncompleteRestoreComponents {
			if !labels[label] {
				uncovered = append(uncovered, label)
			}
		}
		if len(uncovered) > 0 {
			return fmt.Errorf("the incomplete restore %s also rewrote %s, which snapshot %s does not restore; "+
				"restore a snapshot that holds them, or accept the environment's data as it is",
				held, strings.Join(uncovered, ", "), manifest.SnapshotID)
		}
		log.Info(ctx, fmt.Sprintf("replaces the incomplete restore %s", held))
	}
	bctx.listings = map[string]bucketListing{}
	for _, component := range restored {
		listing, err := verifyComponent(ctx, log, bctx.target, manifest.SnapshotID, component)
		if err != nil {
			return err
		}
		if component.Kind == ComponentBucket {
			bctx.listings[componentLabel(component)] = listing
		}
	}
	log.Info(ctx, fmt.Sprintf("snapshot verified: %d of %d components to restore",
		len(restored), len(manifest.Components)))
	return nil
}

// stopEnvironment persists the down decision together with the restore
// hold and waits until the environment's workloads are gone. The kernel
// does the actual teardown; with the adoption guard it journals nothing
// into this run, so the wait here is the user-visible narration.
func (c *Controller) stopEnvironment(ctx context.Context, log *journal.StepLog, row *store.Backup, restored []Component) error {
	// Capture-then-down: MarkEnvironmentRestoring NULLs both revision
	// pointers, which is why the resume revision was captured at accept
	// time. The hold it records is what keeps a deployment from resuming
	// the environment until this restore, or a later one, completes.
	labels := make([]string, 0, len(restored))
	for _, component := range restored {
		labels = append(labels, componentLabel(component))
	}
	unlock, err := c.deps.Store.LockEnvironment(ctx, row.EnvironmentID)
	if err != nil {
		return err
	}
	marked, err := c.deps.Store.MarkEnvironmentRestoring(ctx, store.MarkEnvironmentRestoringParams{
		EnvironmentID: row.EnvironmentID, RestoreID: row.ID, Components: labels,
	})
	unlock()
	if err != nil {
		return fmt.Errorf("mark environment down: %w", err)
	}
	if marked == 0 {
		return errors.New("the environment is releasing and cannot be restored")
	}
	if c.deps.Enqueue != nil {
		c.deps.Enqueue(row.EnvironmentID)
	}
	log.Info(ctx, "deployments are refused until the restore completes")
	log.Info(ctx, "waiting for workloads to stop")

	namespace := kubernetes.NamespaceName(row.EnvironmentID.String())
	selector := kubernetes.EnvironmentSelector(row.EnvironmentID.String())
	deadline := time.Now().Add(c.cfg.JobTimeout)
	for time.Now().Before(deadline) {
		deployments, err := c.deps.Kube.Clientset.AppsV1().Deployments(namespace).
			List(ctx, metav1.ListOptions{LabelSelector: selector})
		if err != nil {
			return fmt.Errorf("list deployments: %w", err)
		}
		var running int
		pods, err := c.deps.Kube.Clientset.CoreV1().Pods(namespace).
			List(ctx, metav1.ListOptions{LabelSelector: selector})
		if err != nil {
			return fmt.Errorf("list pods: %w", err)
		}
		for _, pod := range pods.Items {
			if pod.Status.Phase == corev1.PodRunning || pod.Status.Phase == corev1.PodPending {
				running++
			}
		}
		if len(deployments.Items) == 0 && running == 0 {
			log.Info(ctx, "workloads stopped; volumes and databases remain")
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(stopPollInterval):
		}
	}
	return errors.New("workloads did not stop in time")
}

// restoreComponent restores one snapshot component into the environment,
// or records a visible skip when the captured revision no longer declares
// the matching service.
func (c *Controller) restoreComponent(ctx context.Context, scope *journal.Scope, bctx *backupContext,
	definition *compiler.ProjectDefinition, component Component) error {
	label := componentLabel(component)
	exists := declaresComponent(definition, component)
	switch component.Kind {
	case ComponentDatabase:
		if !exists {
			scope.Skip(ctx, label, fmt.Sprintf("Restore database %s (no matching service)", component.ServiceKey))
			return nil
		}
		return scope.Step(ctx, label, "Restore database "+component.ServiceKey,
			func(ctx context.Context, log *journal.StepLog) error {
				return c.restoreDatabase(ctx, log, bctx, component)
			})
	case ComponentBucket:
		if !exists {
			scope.Skip(ctx, label, fmt.Sprintf("Restore bucket %s (no matching service)", component.ServiceKey))
			return nil
		}
		return scope.Step(ctx, label, "Restore bucket "+component.ServiceKey,
			func(ctx context.Context, log *journal.StepLog) error {
				return c.restoreBucket(ctx, log, bctx, component)
			})
	default:
		if !exists {
			scope.Skip(ctx, label, fmt.Sprintf("Restore volume %s of %s (no matching service)",
				component.Volume, component.Application))
			return nil
		}
		return scope.Step(ctx, label, fmt.Sprintf("Restore volume %s of %s", component.Volume, component.Application),
			func(ctx context.Context, log *journal.StepLog) error {
				return c.restoreVolume(ctx, log, bctx, component)
			})
	}
}

// restoreDatabase downloads the dump and replays it into the service's
// current tenant identity: the worker container fetches, the pool's own
// postgres image restores as the owning role with extension entries
// filtered (CNPG owns extensions; the tenant role cannot recreate them).
func (c *Controller) restoreDatabase(ctx context.Context, log *journal.StepLog, bctx *backupContext, component Component) error {
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
	identity.WorkerImage = workerImage
	identity.TargetSecret = targetSecretName
	identity.SnapshotObject = component.ObjectKey
	identity.Expect = objectCheck{Bytes: component.Bytes, SHA256: component.SHA256}
	name := jobName("skali-restore", row.ID.String(), "db", component.ServiceKey)
	log.Info(ctx, "restoring into database "+identity.DatabaseName+" in one transaction")
	return c.runJob(ctx, log, renderDatabaseRestoreJob(name, substrate.Namespace, row.ID.String(), identity))
}

// restoreBucket clears the service's current bucket, copies the
// snapshot's objects back through the in-cluster gateway, and lists the
// bucket again to prove it holds exactly the snapshot's keys at their
// sizes.
func (c *Controller) restoreBucket(ctx context.Context, log copyLog, bctx *backupContext, component Component) error {
	row := bctx.row
	if c.deps.Buckets == nil {
		return errors.New("the object-storage substrate is not available")
	}
	// Platform access first: it does not depend on the bucket's own
	// identity, and a restore that cannot write must not fence a bucket it
	// will never reopen.
	destination, bucketName, err := c.openBucket(ctx, row.EnvironmentID, component.ServiceKey)
	if err != nil {
		return err
	}
	// The fence: the bucket's own identity is deleted, so the mirrored
	// keys and every presigned URL signed with them are refused while the
	// contents change underneath; the platform identity alone writes. A
	// failed restore leaves the fence up together with the environment
	// down; re-running the restore is the way forward.
	if err := c.deps.Buckets.FenceBucket(ctx, row.EnvironmentID, component.ServiceKey); err != nil {
		return err
	}
	log.Info(ctx, "bucket fenced: the environment's credentials are refused until the restore completes")
	log.Info(ctx, "clearing bucket "+bucketName)
	cleared, err := destination.RemoveAll(ctx)
	if err != nil {
		return err
	}
	log.Info(ctx, fmt.Sprintf("cleared %d objects from bucket %s", cleared, bucketName))
	restored, restoredBytes, err := copyObjects(ctx, log, bctx.target, destination,
		component.ObjectPrefix, "", component.ObjectCount, c.copyOptions())
	if err != nil {
		return err
	}
	if restored != component.ObjectCount {
		return fmt.Errorf("bucket snapshot is incomplete: expected %d objects, restored %d; the bucket remains fenced", component.ObjectCount, restored)
	}
	log.Info(ctx, fmt.Sprintf("restored %d objects (%d bytes)", restored, restoredBytes))
	// The copy's own count cannot see an object the gateway dropped or one
	// that landed beside the copy; only the bucket's listing can.
	want, verified := bctx.listings[componentLabel(component)]
	if !verified {
		if want, err = listBucket(ctx, bctx.target, component.ObjectPrefix); err != nil {
			return fmt.Errorf("list the snapshot's objects: %w", err)
		}
	}
	got, err := listBucket(ctx, destination, "")
	if err != nil {
		return fmt.Errorf("list the restored bucket: %w; the bucket remains fenced", err)
	}
	if got != want {
		return fmt.Errorf("the restored bucket holds %d objects (%s), the snapshot %d (%s)%s; the bucket remains fenced",
			got.Objects, utils.FormatBytes(got.Bytes), want.Objects, utils.FormatBytes(want.Bytes), sameCount(got, want))
	}
	log.Info(ctx, fmt.Sprintf("bucket %s holds exactly the snapshot's %d objects", bucketName, got.Objects))
	if err := c.deps.Buckets.UnfenceBucket(ctx, row.EnvironmentID, component.ServiceKey); err != nil {
		return err
	}
	log.Info(ctx, "bucket fence lifted")
	return nil
}

// restoreVolume replays the archive into the service's PVC while the
// application is stopped. The worker reads the whole archive once to
// verify it before it clears the previous contents.
func (c *Controller) restoreVolume(ctx context.Context, log *journal.StepLog, bctx *backupContext, component Component) error {
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
	name := jobName("skali-restore", row.ID.String(), "vol", component.Application, component.Volume)
	log.Info(ctx, "restoring volume claim "+claimName)
	job := renderVolumeRestoreJob(name, namespace, row.ID.String(), workerImage, targetSecretName,
		claimName, component.ObjectKey, objectCheck{Bytes: component.Bytes, SHA256: component.SHA256})
	return c.runJob(ctx, log, job)
}

// sameCount explains a listing mismatch whose counts and bytes agree.
func sameCount(got, want bucketListing) string {
	if got.Objects == want.Objects && got.Bytes == want.Bytes {
		return ", but under different keys or sizes"
	}
	return ""
}

// awaitReconverge polls until the resumed revision is active again. On
// timeout the run fails but the target stays: level-triggered
// reconciliation keeps working toward it, the same policy as a first
// deployment.
func (c *Controller) awaitReconverge(ctx context.Context, log *journal.StepLog, row *store.Backup) error {
	deadline := time.Now().Add(c.cfg.ReconvergeTimeout)
	for time.Now().Before(deadline) {
		target, err := c.deps.Store.GetEnvironmentTarget(ctx, row.EnvironmentID)
		if err != nil {
			return fmt.Errorf("read environment target: %w", err)
		}
		if target.ActiveRevisionID != nil && row.RevisionID != nil &&
			*target.ActiveRevisionID == *row.RevisionID {
			log.Info(ctx, "environment is healthy on the restored data")
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(reconvergePollInterval):
		}
	}
	return errors.New("the environment did not become healthy in time; reconciliation continues toward the resumed revision")
}
