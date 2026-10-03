package substrate

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/kubetest"
	"github.com/Hinkolas/skali/internal/observe"
	"github.com/Hinkolas/skali/internal/project"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/substrate/seaweed"
	"github.com/Hinkolas/skali/internal/testdb"
)

// TestLiveBucketCredentialRotation drives one rotation through the real
// store on k3d: the commit lands in the credential Secret, the claim
// worker adds the new key beside the old one, mirrors it, and bumps the
// version; both keypairs sign inside the window and drift repair keeps
// them; a fence inside the window brings both back; once the instant
// passes the previous keypair is retired and a presigned URL it signed is
// refused while the new keypair keeps working; a window that expires
// while fenced retires on the fence lift. Requires TEST_KUBECONFIG and
// TEST_DATABASE_URL.
func TestLiveBucketCredentialRotation(t *testing.T) {
	config := kubetest.Config(t)
	pool := testdb.New(t)
	ctx := context.Background()

	client, err := kube.NewFromConfig(config)
	require.NoError(t, err)
	installOperator(t, client)

	st := store.NewStore(pool)
	dbSvc := dbstore.New(st)
	projects := project.New(st)

	suffix := uuid.Must(uuid.NewV7()).String()[24:]
	proj, err := projects.Create(ctx, "demo"+suffix, "", uuid.Nil)
	require.NoError(t, err)
	env, err := projects.CreateEnvironment(ctx, proj.ID, "production", project.EnvironmentOptions{})
	require.NoError(t, err)

	poked := make(chan uuid.UUID, 16)
	controller := New(Deps{
		DB:       dbSvc,
		Cluster:  KubeCluster{Client: client},
		Observed: observe.NewStore(nil),
		Seaweed:  seaweed.NewClient(client, Namespace),
		Enqueue:  func(id uuid.UUID) { poked <- id },
	}, Config{Managed: false})
	cleanupPlatform(t, client)

	namespace := kubernetes.RenderNamespace(proj.Name, "production", env.ID.String())
	_, err = client.Apply(ctx, namespace, false)
	require.NoError(t, err)
	t.Cleanup(func() { deleteNamespace(t, client, namespace.Name) })

	files := driveLiveBucket(t, controller, dbSvc, proj, env, "files")
	bucket := files.BucketName
	claimRow, err := dbSvc.LiveServiceBucketClaim(ctx, env.ID, "files")
	require.NoError(t, err)
	for len(poked) > 0 {
		<-poked
	}

	secrets := client.Clientset.CoreV1().Secrets(Namespace)
	readSecret := func() map[string]string {
		t.Helper()
		secret, err := secrets.Get(ctx, files.CredentialSecret, metav1.GetOptions{})
		require.NoError(t, err)
		data := map[string]string{}
		for key, value := range secret.Data {
			data[key] = string(value)
		}
		for key, value := range secret.Annotations {
			data["@"+key] = value
		}
		return data
	}
	mirror := func() map[string][]byte {
		t.Helper()
		secret, err := client.Clientset.CoreV1().Secrets(namespace.Name).
			Get(ctx, kubernetes.OutputSecretName("buckets", "files"), metav1.GetOptions{})
		require.NoError(t, err)
		return secret.Data
	}
	identityKeys := func() []string {
		t.Helper()
		identities, err := controller.deps.Seaweed.Identities(ctx)
		require.NoError(t, err)
		identity := identities.Find(bucket)
		if identity == nil {
			return nil
		}
		keys := make([]string, 0, len(identity.Credentials))
		for _, credential := range identity.Credentials {
			keys = append(keys, credential.AccessKey)
		}
		return keys
	}
	pass := func() {
		t.Helper()
		_, err := controller.reconcileBucketClaim(ctx, claimRow.ID)
		require.NoError(t, err)
	}
	endpoint := kubetest.PortForward(t, config, Namespace, "app="+seaweed.AllInOneApp, seaweed.S3Port)
	newClient := func(accessKey, secretKey string) *minio.Client {
		s3, err := minio.New(endpoint, &minio.Options{
			Creds:        credentials.NewStaticV4(accessKey, secretKey, ""),
			Region:       seaweed.Region,
			BucketLookup: minio.BucketLookupPath,
		})
		require.NoError(t, err)
		return s3
	}
	web := &http.Client{Timeout: 30 * time.Second}
	writes := func(s3 *minio.Client, key string) bool {
		_, err := s3.PutObject(ctx, bucket, key, strings.NewReader("x"), 1, minio.PutObjectOptions{})
		return err == nil
	}

	// The application holds the original keypair and a presigned URL it
	// signed.
	initial := readSecret()
	require.Equal(t, files.AccessKeyID, initial[credentialAccessKey])
	require.Empty(t, initial[credentialPreviousAccessKey])
	oldKey := newClient(initial[credentialAccessKey], initial[credentialSecretKey])
	signed, err := oldKey.PresignedPutObject(ctx, bucket, "presigned", 10*time.Minute)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status(t, web, http.MethodPut, signed.String(), nil, []byte("1")))

	// The commit: the Secret carries both pairs and the instant; the rows
	// are untouched until the worker runs.
	rotation, err := controller.RotateBucketCredentials(ctx, env.ID, "files", time.Minute)
	require.NoError(t, err)
	committed := readSecret()
	require.Equal(t, rotation.AccessKey, committed[credentialAccessKey])
	require.Equal(t, initial[credentialAccessKey], committed[credentialPreviousAccessKey])
	require.Equal(t, initial[credentialSecretKey], committed[credentialPreviousSecretKey])
	require.Equal(t, rotation.RetireAt.Format(time.RFC3339), committed["@"+AnnotationCredentialRetireAt])
	untouched, err := dbSvc.LiveAllocation(ctx, claimRow.ID)
	require.NoError(t, err)
	require.Equal(t, files.AccessKeyID, untouched.AccessKeyID)
	require.EqualValues(t, 1, untouched.CredentialVersion)

	// The worker's pass: both keys at the store, the new pair in the
	// mirror, the row committed, the environment poked to roll.
	pass()
	require.ElementsMatch(t, []string{rotation.AccessKey, initial[credentialAccessKey]}, identityKeys(),
		"the store accepts both keypairs inside the window")
	require.Equal(t, rotation.AccessKey, string(mirror()["access_key"]))
	require.Equal(t, committed[credentialSecretKey], string(mirror()["secret_key"]))
	bumped, err := dbSvc.LiveAllocation(ctx, claimRow.ID)
	require.NoError(t, err)
	require.Equal(t, rotation.AccessKey, bumped.AccessKeyID)
	require.EqualValues(t, 2, bumped.CredentialVersion)
	require.NotNil(t, bumped.CredentialRetireAt)
	require.True(t, bumped.CredentialRetireAt.Equal(rotation.RetireAt))
	select {
	case id := <-poked:
		require.Equal(t, env.ID, id, "the bump rolls the consumers")
	default:
		t.Fatal("the bump must poke the environment")
	}
	newKey := newClient(committed[credentialAccessKey], committed[credentialSecretKey])
	requireEventually(t, 30*time.Second, func() bool { return writes(newKey, "new") }, "the new keypair writes")
	require.True(t, writes(oldKey, "old"), "the old keypair still writes inside the window")
	require.Equal(t, http.StatusOK, status(t, web, http.MethodPut, signed.String(), nil, []byte("2")),
		"a URL signed with the old keypair works inside the window")

	// Nothing of the new secret key leaks into a row: scan every durable
	// text-ish column for it, as the provisioning test does for the first.
	columns, err := pool.Query(ctx, `
		SELECT table_name, column_name FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND data_type IN ('text', 'jsonb', 'character varying')`)
	require.NoError(t, err)
	type column struct{ table, name string }
	var scan []column
	for columns.Next() {
		var c column
		require.NoError(t, columns.Scan(&c.table, &c.name))
		scan = append(scan, c)
	}
	columns.Close()
	require.NotEmpty(t, scan)
	for _, c := range scan {
		var count int
		require.NoError(t, pool.QueryRow(ctx, fmt.Sprintf(
			`SELECT count(*) FROM %q WHERE %q::text LIKE '%%' || $1 || '%%'`,
			c.table, c.name), committed[credentialSecretKey]).Scan(&count))
		require.Zero(t, count, "secret key leaked into %s.%s", c.table, c.name)
	}

	// Drift repair inside the window keeps both keys and bumps nothing.
	pass()
	require.ElementsMatch(t, []string{rotation.AccessKey, initial[credentialAccessKey]}, identityKeys())
	steady, err := dbSvc.LiveAllocation(ctx, claimRow.ID)
	require.NoError(t, err)
	require.EqualValues(t, 2, steady.CredentialVersion)
	require.Empty(t, poked, "a settled pass pokes nobody")

	// A restore inside the window: the fence removes the identity, the
	// lift brings both keypairs back.
	require.NoError(t, controller.FenceBucket(ctx, env.ID, "files"))
	require.Nil(t, identityKeys())
	require.NoError(t, controller.UnfenceBucket(ctx, env.ID, "files"))
	require.ElementsMatch(t, []string{rotation.AccessKey, initial[credentialAccessKey]}, identityKeys(),
		"a fence lift inside the window restores both keypairs")

	// The instant passes (rewritten into the past, so the test does not
	// wait a minute): the probe notices and wakes the worker, whose pass
	// retires the previous keypair everywhere.
	expired, err := secrets.Get(ctx, files.CredentialSecret, metav1.GetOptions{})
	require.NoError(t, err)
	expired.Annotations[AnnotationCredentialRetireAt] = time.Now().Add(-time.Second).UTC().Format(time.RFC3339)
	_, err = secrets.Update(ctx, expired, metav1.UpdateOptions{})
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE bucket_allocations SET credential_retire_at = now() - interval '1 second' WHERE id = $1`, files.ID)
	require.NoError(t, err)
	// No worker loop runs here, so the queue still holds what earlier
	// passes enqueued; drain it to see exactly what the probe adds.
	for controller.queue.Len() > 0 {
		key, _ := controller.queue.Get()
		controller.queue.Done(key)
	}
	_, err = controller.SeaweedProbe()(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, controller.queue.Len(), "the probe wakes the worker for a passed instant")
	woken, _ := controller.queue.Get()
	controller.queue.Done(woken)
	require.Equal(t, workKey{kind: workBucket, id: claimRow.ID}, woken)
	pass()
	require.Equal(t, []string{rotation.AccessKey}, identityKeys(), "the previous keypair is retired")
	retired := readSecret()
	require.Equal(t, rotation.AccessKey, retired[credentialAccessKey])
	require.NotContains(t, retired, credentialPreviousAccessKey)
	require.NotContains(t, retired, credentialPreviousSecretKey)
	require.NotContains(t, retired, "@"+AnnotationCredentialRetireAt)
	finished, err := dbSvc.LiveAllocation(ctx, claimRow.ID)
	require.NoError(t, err)
	require.Nil(t, finished.CredentialRetireAt)
	require.EqualValues(t, 2, finished.CredentialVersion)
	requireEventually(t, 30*time.Second, func() bool {
		return status(t, web, http.MethodPut, signed.String(), nil, []byte("3")) == http.StatusForbidden
	}, "a URL signed with the retired keypair is refused")
	require.False(t, writes(oldKey, "old-again"), "the retired keypair is refused")
	require.True(t, writes(newKey, "new-again"), "the current keypair keeps working")

	// A second rotation whose window expires while a restore holds the
	// bucket: the fence lift derives the one-key set, the next pass cleans
	// the Secret and the row.
	second, err := controller.RotateBucketCredentials(ctx, env.ID, "files", time.Minute)
	require.NoError(t, err)
	pass()
	require.ElementsMatch(t, []string{second.AccessKey, rotation.AccessKey}, identityKeys())
	third, err := dbSvc.LiveAllocation(ctx, claimRow.ID)
	require.NoError(t, err)
	require.EqualValues(t, 3, third.CredentialVersion)
	require.NoError(t, controller.FenceBucket(ctx, env.ID, "files"))
	expired, err = secrets.Get(ctx, files.CredentialSecret, metav1.GetOptions{})
	require.NoError(t, err)
	expired.Annotations[AnnotationCredentialRetireAt] = time.Now().Add(-time.Second).UTC().Format(time.RFC3339)
	_, err = secrets.Update(ctx, expired, metav1.UpdateOptions{})
	require.NoError(t, err)
	require.NoError(t, controller.UnfenceBucket(ctx, env.ID, "files"))
	require.Equal(t, []string{second.AccessKey}, identityKeys(), "a fence lift past the instant brings back the current keypair only")
	pass()
	cleaned := readSecret()
	require.NotContains(t, cleaned, credentialPreviousAccessKey)
	require.NotContains(t, cleaned, "@"+AnnotationCredentialRetireAt)
	settled, err := dbSvc.LiveAllocation(ctx, claimRow.ID)
	require.NoError(t, err)
	require.Nil(t, settled.CredentialRetireAt)
	require.Equal(t, second.AccessKey, settled.AccessKeyID)
}
