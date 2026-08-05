package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Hinkolas/skali/internal/bundle"
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

	jobPollInterval = 2 * time.Second
	jobTTLSeconds   = int64(3600)
	jobLogTail      = 40
)

// resolveWorkerImage picks the image backup Jobs run the data mover in:
// the configured override, else the daemon's own Deployment image, which
// exists everywhere the bundle deployed skalid (production and local dev
// alike).
func (c *Controller) resolveWorkerImage(ctx context.Context) (string, error) {
	if c.cfg.WorkerImage != "" {
		return c.cfg.WorkerImage, nil
	}
	deployment, err := c.deps.Kube.Clientset.AppsV1().Deployments(bundle.Namespace).
		Get(ctx, "skalid", metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("backup: resolve worker image from the skalid deployment: %w", err)
	}
	for _, container := range deployment.Spec.Template.Spec.Containers {
		if container.Name == "skalid" {
			return container.Image, nil
		}
	}
	if len(deployment.Spec.Template.Spec.Containers) > 0 {
		return deployment.Spec.Template.Spec.Containers[0].Image, nil
	}
	return "", errors.New("backup: the skalid deployment has no containers")
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
		if _, err := secrets.Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("backup: update target secret in %s: %w", namespace, err)
		}
	}
	return nil
}

func (c *Controller) deleteTargetSecret(ctx context.Context, namespace string) {
	err := c.deps.Kube.Clientset.CoreV1().Secrets(namespace).
		Delete(ctx, targetSecretName, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		_ = err
	}
}

// jobName builds a DNS-safe Job name under the 63-character cap.
func jobName(parts ...string) string {
	value := strings.ToLower(strings.Join(parts, "-"))
	if len(value) <= 63 {
		return value
	}
	sum := sha256.Sum256([]byte(value))
	return strings.TrimRight(value[:54], "-") + "-" + hex.EncodeToString(sum[:4])
}

// shortID mirrors the substrate's identity suffix: the last eight hex
// characters of a v7 UUID (the leading ones are timestamp and collide).
func shortID(id string) string {
	compact := strings.ReplaceAll(id, "-", "")
	if len(compact) <= 8 {
		return compact
	}
	return compact[len(compact)-8:]
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
					Name:  "dump",
					Image: identity.PostgresImage,
					Command: []string{"pg_dump", "-Fc", "--no-owner", "--no-privileges",
						"-f", "/work/db.dump"},
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

// databaseRestoreScript replays a dump into the tenant's database as its
// owning role. The list filter drops EXTENSION and COMMENT entries: CNPG
// declares extensions on the cluster and the tenant role can neither drop
// nor recreate them, while --clean plus --if-exists resets everything the
// role owns.
const databaseRestoreScript = `set -e
pg_restore -l /work/db.dump | grep -vE ' (EXTENSION|COMMENT) ' > /work/list
exec pg_restore --clean --if-exists --no-owner --no-privileges --exit-on-error -L /work/list -d "$PGDATABASE" /work/db.dump
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
					Args: []string{"backup-worker", "download",
						"--key", identity.SnapshotObject, "--file", "/work/db.dump"},
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
					Name:    "worker",
					Image:   workerImage,
					Args:    args,
					EnvFrom: workerEnvFrom(targetSecret),
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
// into the step log, and deletes it. A leftover Job of the same name (a
// previous failed attempt) is removed first so the run is idempotent.
func (c *Controller) runJob(ctx context.Context, log *stepLog, job *batchv1.Job) error {
	spec := c.jobSpec(job.Spec.Template.Spec)
	job.Spec.BackoffLimit = spec.BackoffLimit
	job.Spec.ActiveDeadlineSeconds = spec.ActiveDeadlineSeconds
	job.Spec.TTLSecondsAfterFinished = spec.TTLSecondsAfterFinished
	job.Spec.Template.Spec = spec.Template.Spec

	jobs := c.deps.Kube.Clientset.BatchV1().Jobs(job.Namespace)
	if err := c.deleteJobAndWait(ctx, job.Namespace, job.Name); err != nil {
		return err
	}
	if _, err := jobs.Create(ctx, job, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create job %s: %w", job.Name, err)
	}

	ticker := time.NewTicker(jobPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		current, err := jobs.Get(ctx, job.Name, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("watch job %s: %w", job.Name, err)
		}
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
func (c *Controller) tailJobInto(ctx context.Context, log *stepLog, namespace, name string, failed bool) {
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
	propagation := metav1.DeletePropagationBackground
	err := c.deps.Kube.Clientset.BatchV1().Jobs(namespace).Delete(ctx, name,
		metav1.DeleteOptions{PropagationPolicy: &propagation})
	if err != nil && !apierrors.IsNotFound(err) {
		_ = err
	}
}

// deleteJobAndWait removes a leftover Job of the same name and waits until
// it is gone: creating over a terminating Job fails.
func (c *Controller) deleteJobAndWait(ctx context.Context, namespace, name string) error {
	jobs := c.deps.Kube.Clientset.BatchV1().Jobs(namespace)
	if _, err := jobs.Get(ctx, name, metav1.GetOptions{}); apierrors.IsNotFound(err) {
		return nil
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
