package backup

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Hinkolas/skali/internal/compiler"
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

// CreateRestore accepts a stop-first restore of one snapshot into the
// environment. The environment must be active with a target revision: the
// restore stops it, moves data, and resumes exactly that revision. The
// snapshot manifest is verified to exist before anything is accepted.
func (c *Controller) CreateRestore(ctx context.Context, environmentID uuid.UUID, snapshotID, actor string) (*CreateResult, error) {
	names, err := c.environmentNames(ctx, environmentID)
	if err != nil {
		return nil, err
	}
	target, err := c.deps.Store.GetEnvironmentTarget(ctx, environmentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrEnvironmentNotFound
		}
		return nil, fmt.Errorf("backup: get environment target: %w", err)
	}
	if target.State != "active" || target.TargetRevisionID == nil {
		return nil, ErrEnvironmentNotActive
	}

	credentials, err := c.deps.Targets.credentials(ctx, DefaultTargetName)
	if err != nil {
		return nil, err
	}
	targetStore, err := newObjectStore(targetLocation(credentials))
	if err != nil {
		return nil, err
	}
	manifestObjectKey := manifestKey(credentials.Prefix, names.project, names.environment, snapshotID)
	if _, err := targetStore.Stat(ctx, manifestObjectKey); err != nil {
		if errors.Is(err, errNotFound) {
			return nil, ErrSnapshotNotFound
		}
		return nil, &TargetUnreachableError{Err: err}
	}

	run, err := c.deps.Journal.CreateRun(ctx, journal.RunInput{
		Kind:          KindRestore,
		ProjectID:     names.projectID,
		EnvironmentID: environmentID,
		Actor:         actor,
	})
	if err != nil {
		return nil, fmt.Errorf("backup: create run: %w", err)
	}
	if err := c.deps.Journal.StartRun(ctx, run.ID); err != nil {
		if discardErr := c.deps.Journal.DiscardRun(ctx, run.ID); discardErr != nil {
			_ = discardErr
		}
		if errors.Is(err, journal.ErrRunConflict) {
			return nil, ErrBackupInFlight
		}
		return nil, fmt.Errorf("backup: start run: %w", err)
	}

	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("backup: generate id: %w", err)
	}
	runID := run.ID
	row, err := c.deps.Store.CreateBackup(ctx, store.CreateBackupParams{
		ID:              id,
		Kind:            KindRestore,
		EnvironmentID:   environmentID,
		ProjectName:     names.project,
		EnvironmentName: names.environment,
		// The revision the environment resumes after data movement.
		RevisionID: target.TargetRevisionID,
		RunID:      &runID,
	})
	if err != nil {
		_ = c.deps.Journal.FinishRun(ctx, run.ID, journal.RunFailed)
		return nil, fmt.Errorf("backup: create row: %w", err)
	}
	if err := c.deps.Store.SetBackupSnapshotKey(ctx, store.SetBackupSnapshotKeyParams{
		ID: row.ID, SnapshotKey: manifestObjectKey,
	}); err != nil {
		_ = c.deps.Journal.FinishRun(ctx, run.ID, journal.RunFailed)
		return nil, fmt.Errorf("backup: record snapshot key: %w", err)
	}
	c.Enqueue(row.ID)
	return &CreateResult{BackupID: row.ID, RunID: run.ID}, nil
}

// executeRestore drives one stop-first restore: load the manifest, stop
// the environment's workloads, restore every matched component, resume the
// captured revision, and wait for reconvergence. On failure the
// environment deliberately stays down: resuming applications over
// half-restored data is worse than visible downtime, and re-running the
// restore is idempotent.
func (c *Controller) executeRestore(ctx context.Context, scope *runScope, row *store.Backup) error {
	credentials, err := c.deps.Targets.credentials(ctx, DefaultTargetName)
	if err != nil {
		return err
	}
	bctx := &backupContext{row: row, credentials: credentials}
	if err := scope.step(ctx, "target", "Check backup target", func(ctx context.Context, log *stepLog) error {
		bctx.target, err = newObjectStore(targetLocation(credentials))
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
	if err := scope.step(ctx, "snapshot", "Load snapshot manifest", func(ctx context.Context, log *stepLog) error {
		manifest, err = c.readManifest(ctx, bctx.target, row.SnapshotKey)
		if err != nil {
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
	revisionDoc, err := c.deps.Deploy.GetRevision(ctx, *row.RevisionID)
	if err != nil {
		return fmt.Errorf("backup: load revision: %w", err)
	}

	if err := scope.step(ctx, "stop", "Stop environment workloads", func(ctx context.Context, log *stepLog) error {
		return c.stopEnvironment(ctx, log, row)
	}); err != nil {
		return err
	}

	for _, component := range manifest.Components {
		if scope.cancelled(ctx) {
			return errCancelled
		}
		if err := c.restoreComponent(ctx, scope, bctx, &revisionDoc.Definition, component); err != nil {
			return err
		}
	}

	if err := scope.step(ctx, "start", "Resume environment", func(ctx context.Context, log *stepLog) error {
		moved, err := c.deps.Store.SetEnvironmentTarget(ctx, store.SetEnvironmentTargetParams{
			EnvironmentID:    row.EnvironmentID,
			TargetRevisionID: row.RevisionID,
		})
		if err != nil {
			return fmt.Errorf("re-promote revision: %w", err)
		}
		if moved == 0 {
			return errors.New("the environment is releasing; nothing to resume")
		}
		if c.deps.Enqueue != nil {
			c.deps.Enqueue(row.EnvironmentID)
		}
		log.Info(ctx, "target restored to revision "+utils.ShortChecksum(revisionDoc.Checksum))
		return nil
	}); err != nil {
		return err
	}

	return scope.step(ctx, "reconverge", "Wait for environment health", func(ctx context.Context, log *stepLog) error {
		return c.awaitReconverge(ctx, log, row)
	})
}

// stopEnvironment persists the down decision and waits until the
// environment's workloads are gone. The kernel does the actual teardown;
// with the adoption guard it journals nothing into this run, so the wait
// here is the user-visible narration.
func (c *Controller) stopEnvironment(ctx context.Context, log *stepLog, row *store.Backup) error {
	// Capture-then-down: MarkEnvironmentDown NULLs both revision pointers,
	// which is why the resume revision was captured at accept time.
	marked, err := c.deps.Store.MarkEnvironmentDown(ctx, row.EnvironmentID)
	if err != nil {
		return fmt.Errorf("mark environment down: %w", err)
	}
	if marked == 0 {
		return errors.New("the environment is releasing and cannot be restored")
	}
	if c.deps.Enqueue != nil {
		c.deps.Enqueue(row.EnvironmentID)
	}
	log.Info(ctx, "waiting for workloads to stop")

	namespace := kubernetes.NamespaceName(row.ProjectName, row.EnvironmentName)
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
func (c *Controller) restoreComponent(ctx context.Context, scope *runScope, bctx *backupContext,
	definition *compiler.ProjectDefinition, component Component) error {
	label := componentLabel(component)
	switch component.Kind {
	case ComponentDatabase:
		if _, exists := definition.Databases[component.ServiceKey]; !exists {
			scope.skip(ctx, label, fmt.Sprintf("Restore database %s (no matching service)", component.ServiceKey))
			return nil
		}
		return scope.step(ctx, label, "Restore database "+component.ServiceKey,
			func(ctx context.Context, log *stepLog) error {
				return c.restoreDatabase(ctx, log, bctx, component)
			})
	case ComponentBucket:
		if _, exists := definition.Buckets[component.ServiceKey]; !exists {
			scope.skip(ctx, label, fmt.Sprintf("Restore bucket %s (no matching service)", component.ServiceKey))
			return nil
		}
		return scope.step(ctx, label, "Restore bucket "+component.ServiceKey,
			func(ctx context.Context, log *stepLog) error {
				return c.restoreBucket(ctx, log, bctx, component)
			})
	default:
		application, exists := definition.Applications[component.Application]
		if exists {
			_, exists = application.Volumes[component.Volume]
		}
		if !exists {
			scope.skip(ctx, label, fmt.Sprintf("Restore volume %s of %s (no matching service)",
				component.Volume, component.Application))
			return nil
		}
		return scope.step(ctx, label, fmt.Sprintf("Restore volume %s of %s", component.Volume, component.Application),
			func(ctx context.Context, log *stepLog) error {
				return c.restoreVolume(ctx, log, bctx, component)
			})
	}
}

// restoreDatabase downloads the dump and replays it into the service's
// current tenant identity: the worker container fetches, the pool's own
// postgres image restores as the owning role with extension entries
// filtered (CNPG owns extensions; the tenant role cannot recreate them).
func (c *Controller) restoreDatabase(ctx context.Context, log *stepLog, bctx *backupContext, component Component) error {
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
	name := jobName("skali-restore", utils.ShortID(row.ID), "db", component.ServiceKey)
	log.Info(ctx, "restoring into database "+identity.DatabaseName)
	return c.runJob(ctx, log, renderDatabaseRestoreJob(name, substrate.Namespace, row.ID.String(), identity))
}

// restoreBucket clears the service's current bucket and copies the
// snapshot's objects back through the in-cluster gateway.
func (c *Controller) restoreBucket(ctx context.Context, log *stepLog, bctx *backupContext, component Component) error {
	row := bctx.row
	destination, bucketName, err := c.openServiceBucket(ctx, row.EnvironmentID, component.ServiceKey)
	if err != nil {
		return err
	}
	log.Info(ctx, "clearing bucket "+bucketName)
	if err := destination.List(ctx, "", func(info objectInfo) error {
		return destination.Remove(ctx, info.Key)
	}); err != nil {
		return err
	}
	var restored, restoredBytes int64
	err = bctx.target.List(ctx, component.ObjectPrefix, func(info objectInfo) error {
		reader, err := bctx.target.Get(ctx, info.Key)
		if err != nil {
			return err
		}
		defer reader.Close()
		key := info.Key[len(component.ObjectPrefix):]
		if err := destination.Put(ctx, key, reader, info.Size); err != nil {
			return err
		}
		restored++
		restoredBytes += info.Size
		if component.ObjectCount > 0 && restored%16 == 0 {
			log.Progress(ctx, restored, component.ObjectCount)
		}
		return nil
	})
	if err != nil {
		return err
	}
	log.Info(ctx, fmt.Sprintf("restored %d objects (%d bytes)", restored, restoredBytes))
	return nil
}

// restoreVolume replays the archive into the service's PVC while the
// application is stopped; the worker clears previous contents first.
func (c *Controller) restoreVolume(ctx context.Context, log *stepLog, bctx *backupContext, component Component) error {
	row := bctx.row
	workerImage, err := c.workerImageFor(ctx, bctx)
	if err != nil {
		return err
	}
	namespace := kubernetes.NamespaceName(row.ProjectName, row.EnvironmentName)
	if err := c.ensureTargetSecret(ctx, namespace, bctx.credentials); err != nil {
		return err
	}
	claimName := kubernetes.VolumeClaimName(row.ProjectName, component.Application, component.Volume)
	name := jobName("skali-restore", utils.ShortID(row.ID), "vol", component.Application, component.Volume)
	log.Info(ctx, "restoring volume claim "+claimName)
	job := renderVolumeJob(name, namespace, row.ID.String(), workerImage, targetSecretName,
		claimName, component.ObjectKey, true)
	return c.runJob(ctx, log, job)
}

// awaitReconverge polls until the resumed revision is active again. On
// timeout the run fails but the target stays: level-triggered
// reconciliation keeps working toward it, the same policy as a first
// deployment.
func (c *Controller) awaitReconverge(ctx context.Context, log *stepLog, row *store.Backup) error {
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
