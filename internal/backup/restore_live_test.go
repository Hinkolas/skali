package backup

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/minio/minio-go/v7"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/kubetest"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/substrate/seaweed"
)

// livePostgresImage is a pool image the test cluster already holds.
const livePostgresImage = "ghcr.io/cloudnative-pg/postgresql:17.9-system-trixie"

// livePostgres runs a throwaway PostgreSQL server in namespace whose
// "tenant" role owns two databases: app, the restore's destination, and
// snap, which the snapshot is dumped from. It returns the in-cluster host
// and a connection to app from the test.
func livePostgres(t *testing.T, ctx context.Context, client *kube.Client, namespace string) (string, *pgx.Conn) {
	t.Helper()
	labels := map[string]string{"app": "restore-postgres"}
	start := "initdb -D /tmp/pg -U postgres --auth=trust >/dev/null && " +
		"echo 'host all all 0.0.0.0/0 trust' >> /tmp/pg/pg_hba.conf && " +
		"exec postgres -D /tmp/pg -c listen_addresses='*' -c unix_socket_directories=/tmp"
	_, err := client.Clientset.CoreV1().Pods(namespace).Create(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "postgres", Labels: labels},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "postgres", Image: livePostgresImage, ImagePullPolicy: corev1.PullIfNotPresent,
			Command: []string{"/bin/sh", "-c", start},
			Ports:   []corev1.ContainerPort{{ContainerPort: 5432}},
			ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{
				Command: []string{"pg_isready", "-h", "127.0.0.1", "-U", "postgres"}}}, PeriodSeconds: 1},
		}}},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = client.Clientset.CoreV1().Services(namespace).Create(ctx, &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "postgres"},
		Spec: corev1.ServiceSpec{Selector: labels, Ports: []corev1.ServicePort{
			{Port: 5432, TargetPort: intstr.FromInt32(5432)}}},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	address := kubetest.PortForward(t, client.Config, namespace, "app=restore-postgres", 5432)

	admin, err := pgx.Connect(ctx, fmt.Sprintf("postgres://postgres@%s/postgres?sslmode=disable", address))
	require.NoError(t, err)
	defer admin.Close(ctx)
	for _, statement := range []string{
		"CREATE ROLE tenant LOGIN PASSWORD 'tenant'",
		"CREATE DATABASE app OWNER tenant",
		"CREATE DATABASE snap OWNER tenant",
	} {
		_, err := admin.Exec(ctx, statement)
		require.NoError(t, err, statement)
	}
	snap, err := pgx.Connect(ctx, fmt.Sprintf("postgres://tenant:tenant@%s/snap?sslmode=disable", address))
	require.NoError(t, err)
	defer snap.Close(ctx)
	_, err = snap.Exec(ctx, `CREATE TABLE notes (id int PRIMARY KEY, body text);
		INSERT INTO notes SELECT g, 'snapshot ' || g FROM generate_series(1, 100) g;
		CREATE TABLE blobs (id int PRIMARY KEY, data text);
		INSERT INTO blobs SELECT g, repeat(md5(g::text), 50) FROM generate_series(1, 20000) g`)
	require.NoError(t, err)
	app, err := pgx.Connect(ctx, fmt.Sprintf("postgres://tenant:tenant@%s/app?sslmode=disable", address))
	require.NoError(t, err)
	t.Cleanup(func() { _ = app.Close(context.Background()) })
	_, err = app.Exec(ctx, `CREATE TABLE notes (id int PRIMARY KEY, body text);
		INSERT INTO notes VALUES (1, 'live data');
		CREATE TABLE blobs (id int PRIMARY KEY, data text);
		INSERT INTO blobs VALUES (1, 'live blob')`)
	require.NoError(t, err)
	_, err = client.Clientset.CoreV1().Secrets(namespace).Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "tenant"},
		StringData: map[string]string{"username": "tenant", "password": "tenant"},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	return "postgres." + namespace + ".svc.cluster.local", app
}

// TestLiveDatabaseRestoreIsOneTransaction runs the restore Job's own
// script with a real pg_restore: a dump whose data ends early (the table
// of contents still reads, the restore fails halfway) leaves the database
// exactly as it was, where the restore had already dropped and recreated
// the tables before; the complete dump restores. The snapshot's dump comes
// from an init container in place of the download, which the worker
// verifies on its own. Requires TEST_KUBECONFIG and TEST_DATABASE_URL.
func TestLiveDatabaseRestoreIsOneTransaction(t *testing.T) {
	config := kubetest.Config(t)
	f := newServiceFixture(t)
	ctx := context.Background()
	client, err := kube.NewFromConfig(config)
	require.NoError(t, err)
	f.controller.deps.Kube = client
	f.controller.cfg.JobTimeout = 5 * time.Minute
	namespace := kubetest.Namespace(t, client.Clientset)
	host, app := livePostgres(t, ctx, client, namespace)

	content := func() (int, string, int) {
		var notes, blobs int
		var first string
		require.NoError(t, app.QueryRow(ctx, `SELECT (SELECT count(*) FROM notes),
			coalesce((SELECT body FROM notes WHERE id = 1), ''), (SELECT count(*) FROM blobs)`).Scan(&notes, &first, &blobs))
		return notes, first, blobs
	}
	run, err := f.journal.BeginRun(ctx, journal.RunInput{
		Kind: KindRestore, ProjectID: f.projectID, EnvironmentID: f.environmentID, Actor: "tester",
	})
	require.NoError(t, err)
	scope := journal.NewScope(f.journal, nil, run.ID)
	restore := func(name, cut string) error {
		identity := databaseJobIdentity{
			Host: host, Port: 5432, DatabaseName: "app", SecretName: "tenant",
			PostgresImage: livePostgresImage, WorkerImage: livePostgresImage,
		}
		job := renderDatabaseRestoreJob(name, namespace, run.ID.String(), identity)
		snapshot := job.Spec.Template.Spec.Containers[0].DeepCopy()
		snapshot.Name = "snapshot"
		snapshot.Command = []string{"/bin/sh", "-c", databaseReadyWait +
			"set -e\nPGDATABASE=snap pg_dump -Fc --no-owner --no-privileges -f /work/db.dump\n" + cut}
		job.Spec.Template.Spec.InitContainers = []corev1.Container{*snapshot}
		return scope.Step(ctx, name, "Restore "+name, func(ctx context.Context, log *journal.StepLog) error {
			return f.controller.runJob(ctx, log, job)
		})
	}

	// The data ends 4 KiB early: the table of contents reads, and the
	// restore fails on the last table's data, after it dropped and
	// recreated both tables.
	err = restore("truncated", "truncate -s -4096 /work/db.dump\n")
	require.ErrorContains(t, err, "failed")
	notes, first, blobs := content()
	require.Equal(t, 1, notes)
	require.Equal(t, "live data", first)
	require.Equal(t, 1, blobs, "the failed restore rolled back")

	require.NoError(t, restore("complete", ""))
	notes, first, blobs = content()
	require.Equal(t, 100, notes)
	require.Equal(t, "snapshot 1", first)
	require.Equal(t, 20000, blobs)
}

// liveSeaweed runs a single SeaweedFS server, the pinned image the
// platform's object store runs, in namespace and returns a function that
// opens one of its buckets through a tunnel, creating the bucket.
func liveSeaweed(t *testing.T, ctx context.Context, client *kube.Client, namespace string) func(bucket string, walk bool) *minioStore {
	t.Helper()
	labels := map[string]string{"app": "restore-seaweed"}
	_, err := client.Clientset.CoreV1().ConfigMaps(namespace).Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "s3"},
		Data: map[string]string{"s3.json": `{"identities":[{"name":"admin",` +
			`"credentials":[{"accessKey":"admin","secretKey":"adminsecret"}],` +
			`"actions":["Admin","Read","Write","List","Tagging"]}]}`},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = client.Clientset.CoreV1().Pods(namespace).Create(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "seaweed", Labels: labels},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name: "seaweed", Image: seaweed.Image, ImagePullPolicy: corev1.PullIfNotPresent,
				Args: []string{"server", "-dir=/data", "-ip.bind=0.0.0.0", "-s3",
					fmt.Sprintf("-s3.port=%d", seaweed.S3Port), "-s3.config=/etc/seaweedfs/s3.json"},
				VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: "/data"},
					{Name: "s3", MountPath: "/etc/seaweedfs"}},
				ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{
					Port: intstr.FromInt32(seaweed.S3Port)}}, PeriodSeconds: 1},
			}},
			Volumes: []corev1.Volume{
				{Name: "data", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
				{Name: "s3", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: "s3"}}}},
			},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	address := kubetest.PortForward(t, client.Config, namespace, "app=restore-seaweed", seaweed.S3Port)
	return func(bucket string, walk bool) *minioStore {
		store, err := newObjectStore(s3Location{Endpoint: "http://" + address, Region: seaweed.Region,
			Bucket: bucket, AccessKey: "admin", SecretKey: "adminsecret"})
		require.NoError(t, err)
		store.walk = walk
		require.Eventually(t, func() bool {
			exists, err := store.client.BucketExists(ctx, bucket)
			if err == nil && !exists {
				err = store.client.MakeBucket(ctx, bucket, minio.MakeBucketOptions{})
			}
			return err == nil
		}, 2*time.Minute, time.Second, "seaweedfs never created bucket %s", bucket)
		return store
	}
}

// TestLiveBucketRestoreListsWhatItWrote backs a bucket up to a target and
// restores it after it changed, on a real SeaweedFS: nested keys, a
// folder marker, and objects in a folder of their own, which SeaweedFS
// keeps as an empty folder once the restore's clear deleted them. A
// listing one level at a time reports such a folder as an object, so a
// clear that left it would fail the restore's check of the bucket against
// the snapshot, and leave a folder the snapshot never had. Requires
// TEST_KUBECONFIG and TEST_DATABASE_URL.
func TestLiveBucketRestoreListsWhatItWrote(t *testing.T) {
	config := kubetest.Config(t)
	f := newServiceFixture(t)
	ctx := context.Background()
	client, err := kube.NewFromConfig(config)
	require.NoError(t, err)
	namespace := kubetest.Namespace(t, client.Clientset)
	open := liveSeaweed(t, ctx, client, namespace)
	live, target := open("live", true), open("backups", false)

	keys := []string{"top.txt", "tree/a/one", "tree/a/b/two", "tree/c/three", "tree/marker/"}
	for _, key := range keys {
		body := "content of " + key
		if strings.HasSuffix(key, "/") {
			body = ""
		}
		require.NoError(t, live.PutWithMeta(ctx, key, strings.NewReader(body), int64(len(body)),
			objectMeta{ContentType: "text/plain"}))
	}

	fence := &fenceProbe{}
	f.controller.deps.Buckets = fence
	f.controller.openBucket = func(context.Context, uuid.UUID, string) (objectStore, string, error) {
		return live, "live", nil
	}
	run, err := f.journal.BeginRun(ctx, journal.RunInput{
		Kind: KindRestore, ProjectID: f.projectID, EnvironmentID: f.environmentID, Actor: "tester",
	})
	require.NoError(t, err)
	scope := journal.NewScope(f.journal, nil, run.ID)
	bctx := &backupContext{
		row:         &store.Backup{EnvironmentID: f.environmentID, ProjectName: "demo", EnvironmentName: "production"},
		credentials: &Credentials{},
		target:      target,
		snapshotID:  "snap",
	}
	component := Component{Kind: ComponentBucket, ServiceKey: "files"}
	require.NoError(t, scope.Step(ctx, "backup", "Back up", func(ctx context.Context, log *journal.StepLog) error {
		return f.controller.backupBucket(ctx, log, bctx, &component)
	}))
	require.EqualValues(t, len(keys), component.ObjectCount)

	// The bucket moves on: an object goes, others arrive, one in a folder
	// of its own that the restore's clear empties.
	require.NoError(t, live.Remove(ctx, "tree/a/one"))
	for _, key := range []string{"stale/deep/x", "later.txt"} {
		require.NoError(t, live.Put(ctx, key, strings.NewReader("new"), 3))
	}

	require.NoError(t, scope.Step(ctx, "restore", "Restore", func(ctx context.Context, log *journal.StepLog) error {
		listing, err := verifyComponent(ctx, log, target, "snap", component)
		if err != nil {
			return err
		}
		bctx.listings = map[string]bucketListing{componentLabel(component): listing}
		return f.controller.restoreBucket(ctx, log, bctx, component)
	}))
	require.True(t, fence.unfenced, "the restored bucket matched its snapshot")
	var restored []string
	live.walk = false
	require.NoError(t, live.List(ctx, "", func(info objectInfo) error {
		restored = append(restored, info.Key)
		return nil
	}))
	require.ElementsMatch(t, keys, restored)
}
