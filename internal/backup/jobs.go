package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/Hinkolas/skali/internal/bundle"
	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/substrate"
)

// Backup Jobs are deliberately invisible to the reconcile kernel: they
// carry the backup label below and never skali.dev/managed or an
// environment label, so the observe informers ignore them and planPrune
// can never delete one mid-operation. The controller supervises its Jobs
// directly by polling, and TTLSecondsAfterFinished reaps anything a dying
// daemon leaves behind.
const (
	backupJobLabel = "skali.dev/backup"
	// targetSecretName is the per-operation Secret carrying the S3 target
	// as SKALI_BACKUP_* keys; Jobs mount it with envFrom. It is created
	// before the first Job of an operation and deleted when the operation
	// ends.
	targetSecretName = "skali-backup-target"

	jobTTLSeconds = int64(3600)
	jobLogTail    = 40
)

// jobPollInterval is how often a running Job's status is read.
var jobPollInterval = 2 * time.Second

// resolveWorkerImage picks the image backup Jobs run the data mover in:
// the configured override, else the daemon's own image.
func (c *Controller) resolveWorkerImage(ctx context.Context) (string, error) {
	image, err := bundle.SkalidImage(ctx, c.deps.Kube.Clientset, c.cfg.WorkerImage)
	if err != nil {
		return "", fmt.Errorf("backup: worker image: %w", err)
	}
	return image, nil
}

// ensureTargetSecret writes the operation's S3 credentials into the Job
// namespace. The keys are the exact SKALI_BACKUP_* names the worker reads,
// so Jobs consume the whole Secret with envFrom.
func (c *Controller) ensureTargetSecret(ctx context.Context, namespace string, credentials *Credentials) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      targetSecretName,
			Namespace: namespace,
			Labels:    map[string]string{backupJobLabel + "-owned": "true"},
		},
		Type: corev1.SecretTypeOpaque,
		StringData: map[string]string{
			"SKALI_BACKUP_ENDPOINT":          credentials.Endpoint,
			"SKALI_BACKUP_REGION":            credentials.Region,
			"SKALI_BACKUP_BUCKET":            credentials.Bucket,
			"SKALI_BACKUP_ACCESS_KEY_ID":     credentials.AccessKeyID,
			"SKALI_BACKUP_SECRET_ACCESS_KEY": credentials.SecretAccessKey,
		},
	}
	secrets := c.deps.Kube.Clientset.CoreV1().Secrets(namespace)
	if _, err := secrets.Create(ctx, secret, metav1.CreateOptions{}); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("backup: create target secret in %s: %w", namespace, err)
		}
		live, err := secrets.Get(ctx, targetSecretName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if live.Labels[backupJobLabel+"-owned"] != "true" {
			return errors.New("backup: refusing unowned target secret")
		}
		secret.ResourceVersion = live.ResourceVersion
		if _, err := secrets.Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("backup: update target secret in %s: %w", namespace, err)
		}
	}
	return nil
}

func (c *Controller) deleteTargetSecret(ctx context.Context, namespace string) {
	secrets := c.deps.Kube.Clientset.CoreV1().Secrets(namespace)
	live, err := secrets.Get(ctx, targetSecretName, metav1.GetOptions{})
	if err != nil || live.Labels[backupJobLabel+"-owned"] != "true" {
		return
	}
	_ = secrets.Delete(ctx, targetSecretName, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &live.UID, ResourceVersion: &live.ResourceVersion}})
}

// jobName builds a DNS-safe Job name under the 63-character cap.
func jobName(parts ...string) string {
	encoded, _ := json.Marshal(parts)
	sum := sha256.Sum256(encoded)
	prefix := strings.ToLower(strings.Join(parts, "-"))
	if len(prefix) > 30 {
		prefix = strings.TrimRight(prefix[:30], "-")
	}
	return prefix + "-" + hex.EncodeToString(sum[:16])
}

func jobMeta(name, namespace, backupID string) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name:      name,
		Namespace: namespace,
		Labels:    map[string]string{backupJobLabel: backupID},
	}
}

func (c *Controller) jobSpec(pod corev1.PodSpec) batchv1.JobSpec {
	backoff := int32(0)
	deadline := int64(c.cfg.JobTimeout.Seconds())
	ttl := int32(jobTTLSeconds)
	automount := false
	pod.RestartPolicy = corev1.RestartPolicyNever
	pod.AutomountServiceAccountToken = &automount
	return batchv1.JobSpec{
		BackoffLimit:            &backoff,
		ActiveDeadlineSeconds:   &deadline,
		TTLSecondsAfterFinished: &ttl,
		Template:                corev1.PodTemplateSpec{Spec: pod},
	}
}

// databaseJobPod is the shared pod shape of database backup and restore: a
// postgres-client container (the pool's own pinned image) and the worker
// container share an emptyDir at /work. On backup the client dumps first
// (initContainer) and the worker uploads; on restore the worker downloads
// first and the client restores.
type databaseJobIdentity struct {
	Host           string
	Port           int32
	DatabaseName   string
	SecretName     string
	PostgresImage  string
	WorkerImage    string
	TargetSecret   string
	SnapshotObject string
	// Expect is what a restore's download must find: the dump's size and
	// checksum as the manifest records them.
	Expect objectCheck
}

// objectCheck is what a worker verifies a snapshot object against before
// its contents are used: the size and, when recorded, the sha256 of the
// stored bytes.
type objectCheck struct {
	Bytes  int64
	SHA256 string
}

// args renders the check as worker flags.
func (c objectCheck) args() []string {
	args := []string{"--size", strconv.FormatInt(c.Bytes, 10)}
	if c.SHA256 != "" {
		args = append(args, "--sha256", c.SHA256)
	}
	return args
}

func databaseClientEnv(identity databaseJobIdentity) []corev1.EnvVar {
	return []corev1.EnvVar{
		{Name: "PGHOST", Value: identity.Host},
		{Name: "PGPORT", Value: strconv.Itoa(int(identity.Port))},
		{Name: "PGDATABASE", Value: identity.DatabaseName},
		{Name: "PGUSER", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: identity.SecretName}, Key: "username"}}},
		{Name: "PGPASSWORD", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: identity.SecretName}, Key: "password"}}},
	}
}

func workerEnvFrom(targetSecret string) []corev1.EnvFromSource {
	return []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{
		LocalObjectReference: corev1.LocalObjectReference{Name: targetSecret}}}}
}

const workVolume = "work"

func workMount() corev1.VolumeMount {
	return corev1.VolumeMount{Name: workVolume, MountPath: "/work"}
}

func renderDatabaseBackupJob(name, namespace, backupID string, identity databaseJobIdentity) *batchv1.Job {
	return &batchv1.Job{
		TypeMeta:   metav1.TypeMeta{APIVersion: "batch/v1", Kind: "Job"},
		ObjectMeta: jobMeta(name, namespace, backupID),
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				InitContainers: []corev1.Container{{
					Name:         "dump",
					Image:        identity.PostgresImage,
					Command:      []string{"/bin/sh", "-c", databaseBackupScript},
					Env:          databaseClientEnv(identity),
					VolumeMounts: []corev1.VolumeMount{workMount()},
				}},
				Containers: []corev1.Container{{
					Name:  "upload",
					Image: identity.WorkerImage,
					Args: []string{"backup-worker", "upload",
						"--file", "/work/db.dump", "--key", identity.SnapshotObject},
					EnvFrom:      workerEnvFrom(identity.TargetSecret),
					VolumeMounts: []corev1.VolumeMount{workMount()},
				}},
				Volumes: []corev1.Volume{{Name: workVolume,
					VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}},
			}},
		},
	}
}

// databaseReadyWait waits briefly for the pool to accept connections
// before the client runs. A fresh pod's first connection can be rejected
// ("connection refused") while the CNI's network policy enforcement still
// registers the pod's address, and Jobs never retry. The loop falls
// through after its budget so a pool that is really down fails with the
// client's own error.
const databaseReadyWait = `for i in $(seq 1 30); do pg_isready -q && break; sleep 1; done
`

const databaseBackupScript = databaseReadyWait + `exec pg_dump -Fc --no-owner --no-privileges -f /work/db.dump
`

// databaseRestoreScript replays a dump into the tenant's database as its
// owning role. Reading the table of contents first refuses an unreadable
// dump before the database is touched. The list filter drops EXTENSION and
// COMMENT entries: CNPG declares extensions on the cluster and the tenant
// role can neither drop nor recreate them, while --clean plus --if-exists
// resets everything the role owns. The restore is one transaction: a
// failure anywhere rolls it back and leaves the database as it was.
const databaseRestoreScript = databaseReadyWait + `set -e
pg_restore -l /work/db.dump > /work/toc
grep -vE ' (EXTENSION|COMMENT) ' /work/toc > /work/list
exec pg_restore --clean --if-exists --no-owner --no-privileges --single-transaction --exit-on-error -L /work/list -d "$PGDATABASE" /work/db.dump
`

func renderDatabaseRestoreJob(name, namespace, backupID string, identity databaseJobIdentity) *batchv1.Job {
	return &batchv1.Job{
		TypeMeta:   metav1.TypeMeta{APIVersion: "batch/v1", Kind: "Job"},
		ObjectMeta: jobMeta(name, namespace, backupID),
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				InitContainers: []corev1.Container{{
					Name:  "download",
					Image: identity.WorkerImage,
					Args: append([]string{"backup-worker", "download",
						"--key", identity.SnapshotObject, "--file", "/work/db.dump"}, identity.Expect.args()...),
					EnvFrom:      workerEnvFrom(identity.TargetSecret),
					VolumeMounts: []corev1.VolumeMount{workMount()},
				}},
				Containers: []corev1.Container{{
					Name:         "restore",
					Image:        identity.PostgresImage,
					Command:      []string{"/bin/sh", "-c", databaseRestoreScript},
					Env:          databaseClientEnv(identity),
					VolumeMounts: []corev1.VolumeMount{workMount()},
				}},
				Volumes: []corev1.Volume{{Name: workVolume,
					VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}},
			}},
		},
	}
}

// renderVolumeRestoreJob is the volume Job of a restore: the worker
// verifies the archive against expect, then replaces the volume's contents
// with it.
func renderVolumeRestoreJob(name, namespace, backupID, workerImage, targetSecret, claimName, objectKey string, expect objectCheck) *batchv1.Job {
	job := renderVolumeJob(name, namespace, backupID, workerImage, targetSecret, claimName, objectKey, true)
	container := &job.Spec.Template.Spec.Containers[0]
	container.Args = append(container.Args, expect.args()...)
	return job
}

func renderVolumeJob(name, namespace, backupID, workerImage, targetSecret, claimName, objectKey string, restore bool) *batchv1.Job {
	verb, readOnly := "tar", true
	if restore {
		verb, readOnly = "untar", false
	}
	args := []string{"backup-worker", verb, "--path", "/data", "--key", objectKey}
	return &batchv1.Job{
		TypeMeta:   metav1.TypeMeta{APIVersion: "batch/v1", Kind: "Job"},
		ObjectMeta: jobMeta(name, namespace, backupID),
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Containers: []corev1.Container{{
					SecurityContext: &corev1.SecurityContext{RunAsUser: new(int64(0)), RunAsGroup: new(int64(0)), AllowPrivilegeEscalation: new(false), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}, Add: []corev1.Capability{"CHOWN", "FOWNER", "DAC_OVERRIDE", "FSETID"}}},
					Name:            "worker",
					Image:           workerImage,
					Args:            args,
					EnvFrom:         workerEnvFrom(targetSecret),
					VolumeMounts: []corev1.VolumeMount{{
						Name: "data", MountPath: "/data", ReadOnly: readOnly}},
				}},
				Volumes: []corev1.Volume{{Name: "data",
					VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: claimName, ReadOnly: readOnly}}}},
			}},
		},
	}
}

// runJob creates the Job, waits for it to finish, folds its useful output
// into the step log, and deletes it. Failed reads of the Job's status are
// retried (readFailures); the Job's own deadline bounds the wait. A leftover Job of the same name (a
// previous failed attempt) is removed first so the run is idempotent.
func (c *Controller) runJob(ctx context.Context, log *journal.StepLog, job *batchv1.Job) error {
	spec := c.jobSpec(job.Spec.Template.Spec)
	job.Spec.BackoffLimit = spec.BackoffLimit
	job.Spec.ActiveDeadlineSeconds = spec.ActiveDeadlineSeconds
	job.Spec.TTLSecondsAfterFinished = spec.TTLSecondsAfterFinished
	job.Spec.Template.Spec = spec.Template.Spec
	// The pods carry the operation's label too, so stopWorkers finds a
	// pod that outlives its Job.
	job.Spec.Template.Labels = map[string]string{backupJobLabel: job.Labels[backupJobLabel]}

	jobs := c.deps.Kube.Clientset.BatchV1().Jobs(job.Namespace)
	if err := c.deleteJobAndWait(ctx, job.Namespace, job.Name); err != nil {
		return err
	}
	if _, err := jobs.Create(ctx, job, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create job %s: %w", job.Name, err)
	}

	var failures readFailures
	wait := jobPollInterval
	for {
		if err := pause(ctx, wait); err != nil {
			return err
		}
		current, err := jobs.Get(ctx, job.Name, metav1.GetOptions{})
		if err != nil {
			if wait, err = failures.retry(ctx, err); err != nil {
				return fmt.Errorf("watch job %s: %w", job.Name, err)
			}
			continue
		}
		failures.ok()
		wait = jobPollInterval
		if current.Status.Succeeded > 0 {
			c.tailJobInto(ctx, log, job.Namespace, job.Name, false)
			c.deleteJob(ctx, job.Namespace, job.Name)
			return nil
		}
		if current.Status.Failed > 0 {
			c.tailJobInto(ctx, log, job.Namespace, job.Name, true)
			reason := jobFailureReason(current)
			c.deleteJob(ctx, job.Namespace, job.Name)
			return fmt.Errorf("job %s failed: %s", job.Name, reason)
		}
	}
}

func jobFailureReason(job *batchv1.Job) string {
	for _, condition := range job.Status.Conditions {
		if condition.Type == batchv1.JobFailed && condition.Status == corev1.ConditionTrue {
			if condition.Reason == "DeadlineExceeded" {
				return "exceeded its timeout"
			}
			if condition.Message != "" {
				return condition.Message
			}
			return condition.Reason
		}
	}
	return "pod failed"
}

// tailJobInto copies the tail of the Job pod's most relevant container log
// into the step: on failure the failed container, otherwise the last one.
func (c *Controller) tailJobInto(ctx context.Context, log *journal.StepLog, namespace, name string, failed bool) {
	pods, err := c.deps.Kube.Clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "batch.kubernetes.io/job-name=" + name,
	})
	if err != nil || len(pods.Items) == 0 {
		return
	}
	newest := 0
	for index := range pods.Items {
		if pods.Items[index].CreationTimestamp.After(pods.Items[newest].CreationTimestamp.Time) {
			newest = index
		}
	}
	pod := &pods.Items[newest]
	container := relevantContainer(pod, failed)
	tail := int64(jobLogTail)
	stream, err := c.deps.Kube.Clientset.CoreV1().Pods(namespace).
		GetLogs(pod.Name, &corev1.PodLogOptions{Container: container, TailLines: &tail}).Stream(ctx)
	if err != nil {
		return
	}
	defer stream.Close()
	data, err := readAll(stream, 256<<10)
	if err != nil {
		return
	}
	for line := range strings.Lines(string(data)) {
		line = strings.TrimRight(line, "\n")
		if line == "" {
			continue
		}
		if failed {
			log.Error(ctx, line)
		} else {
			log.Info(ctx, line)
		}
	}
}

// relevantContainer picks which container's log explains the pod: the
// first non-zero-exit container on failure, the last container otherwise.
func relevantContainer(pod *corev1.Pod, failed bool) string {
	statuses := append(append([]corev1.ContainerStatus{}, pod.Status.InitContainerStatuses...),
		pod.Status.ContainerStatuses...)
	if failed {
		for _, status := range statuses {
			terminated := status.State.Terminated
			if terminated != nil && terminated.ExitCode != 0 {
				return status.Name
			}
			waiting := status.State.Waiting
			if waiting != nil && waiting.Reason != "" && waiting.Reason != "PodInitializing" {
				return status.Name
			}
		}
	}
	if len(pod.Spec.Containers) > 0 {
		return pod.Spec.Containers[len(pod.Spec.Containers)-1].Name
	}
	return ""
}

func (c *Controller) deleteJob(ctx context.Context, namespace, name string) {
	jobs := c.deps.Kube.Clientset.BatchV1().Jobs(namespace)
	live, err := jobs.Get(ctx, name, metav1.GetOptions{})
	if err != nil || live.Labels[backupJobLabel] == "" {
		return
	}
	_ = jobs.Delete(ctx, name, foregroundDelete(live.UID, live.ResourceVersion))
}

// foregroundDelete deletes a Job only after its pods: the Job stays listed,
// marked for deletion, until every pod of it has terminated.
func foregroundDelete(uid types.UID, resourceVersion string) metav1.DeleteOptions {
	propagation := metav1.DeletePropagationForeground
	options := metav1.DeleteOptions{PropagationPolicy: &propagation, Preconditions: &metav1.Preconditions{UID: &uid}}
	if resourceVersion != "" {
		options.Preconditions.ResourceVersion = &resourceVersion
	}
	return options
}

// workerNamespaces are the namespaces an operation's Jobs run in: the
// platform's for databases, the environment's for volumes.
func workerNamespaces(environmentID uuid.UUID) []string {
	return []string{substrate.Namespace, kubernetes.NamespaceName(environmentID.String())}
}

// workerStopWait bounds how long stopWorkers waits for the workers it
// deleted to terminate.
var workerStopWait = 15 * time.Second

// WorkersRunningError: an operation's workers could not be confirmed
// stopped. Left names what is still there; Err is the Kubernetes error
// that kept the check from finishing, if any.
type WorkersRunningError struct {
	OperationID uuid.UUID
	Left        []string
	Err         error
}

func (e *WorkersRunningError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("its workers could not be confirmed stopped: %v", e.Err)
	}
	return "its workers have not stopped: " + strings.Join(e.Left, ", ")
}

func (e *WorkersRunningError) Is(target error) bool { return target == ErrRestoreStillRunning }

// stopWorkers deletes what is left of an operation's Jobs and their pods
// and waits, briefly, until none of them remains: no Job of the operation
// in either namespace, and no pod of it that has not terminated. Only
// then has the operation stopped writing; a restore's terminal row alone
// does not prove it, since the cleanup that concludes it does not wait
// and may have failed. A WorkersRunningError reports what is left after
// the wait, or the Kubernetes error that prevented the check.
func (c *Controller) stopWorkers(ctx context.Context, operationID, environmentID uuid.UUID) error {
	if c.deps.Kube == nil {
		return nil
	}
	deadline := time.Now().Add(workerStopWait)
	for {
		err := c.deleteWorkers(ctx, operationID, environmentID)
		var left []string
		if err == nil {
			left, err = c.liveWorkers(ctx, operationID, environmentID)
		}
		if err == nil && len(left) == 0 {
			return nil
		}
		if !time.Now().Before(deadline) || ctx.Err() != nil {
			return &WorkersRunningError{OperationID: operationID, Left: left, Err: err}
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
}

// deleteWorkers deletes every Job of the operation in the foreground, then
// every pod of it still running: one a Job deleted in the background left
// behind keeps running until it is deleted itself.
func (c *Controller) deleteWorkers(ctx context.Context, operationID, environmentID uuid.UUID) error {
	selector := metav1.ListOptions{LabelSelector: backupJobLabel + "=" + operationID.String()}
	for _, namespace := range workerNamespaces(environmentID) {
		jobs := c.deps.Kube.Clientset.BatchV1().Jobs(namespace)
		list, err := jobs.List(ctx, selector)
		if err != nil {
			return fmt.Errorf("list jobs in %s: %w", namespace, err)
		}
		for _, job := range list.Items {
			if job.DeletionTimestamp != nil {
				continue
			}
			if err := jobs.Delete(ctx, job.Name, foregroundDelete(job.UID, "")); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("delete job %s/%s: %w", namespace, job.Name, err)
			}
		}
		pods := c.deps.Kube.Clientset.CoreV1().Pods(namespace)
		podList, err := pods.List(ctx, selector)
		if err != nil {
			return fmt.Errorf("list pods in %s: %w", namespace, err)
		}
		for _, pod := range podList.Items {
			if podTerminated(&pod) || pod.DeletionTimestamp != nil {
				continue
			}
			if err := pods.Delete(ctx, pod.Name, metav1.DeleteOptions{
				Preconditions: &metav1.Preconditions{UID: &pod.UID}}); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("delete pod %s/%s: %w", namespace, pod.Name, err)
			}
		}
	}
	return nil
}

// liveWorkers names the operation's Jobs that still exist and its pods
// that have not terminated.
func (c *Controller) liveWorkers(ctx context.Context, operationID, environmentID uuid.UUID) ([]string, error) {
	selector := metav1.ListOptions{LabelSelector: backupJobLabel + "=" + operationID.String()}
	var left []string
	for _, namespace := range workerNamespaces(environmentID) {
		jobs, err := c.deps.Kube.Clientset.BatchV1().Jobs(namespace).List(ctx, selector)
		if err != nil {
			return nil, fmt.Errorf("list jobs in %s: %w", namespace, err)
		}
		for _, job := range jobs.Items {
			left = append(left, "job "+namespace+"/"+job.Name)
		}
		pods, err := c.deps.Kube.Clientset.CoreV1().Pods(namespace).List(ctx, selector)
		if err != nil {
			return nil, fmt.Errorf("list pods in %s: %w", namespace, err)
		}
		for _, pod := range pods.Items {
			if !podTerminated(&pod) {
				left = append(left, "pod "+namespace+"/"+pod.Name)
			}
		}
	}
	return left, nil
}

// podTerminated reports whether none of the pod's containers can run
// again: it succeeded or failed.
func podTerminated(pod *corev1.Pod) bool {
	return pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed
}

// deleteJobAndWait removes a leftover Job of the same name and waits until
// it is gone: creating over a terminating Job fails.
func (c *Controller) deleteJobAndWait(ctx context.Context, namespace, name string) error {
	jobs := c.deps.Kube.Clientset.BatchV1().Jobs(namespace)
	live, err := jobs.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if live.Labels[backupJobLabel] == "" {
		return errors.New("backup: refusing unowned job")
	}
	c.deleteJob(ctx, namespace, name)
	deadline := time.Now().Add(time.Minute)
	for time.Now().Before(deadline) {
		if _, err := jobs.Get(ctx, name, metav1.GetOptions{}); apierrors.IsNotFound(err) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return fmt.Errorf("previous job %s did not terminate", name)
}
