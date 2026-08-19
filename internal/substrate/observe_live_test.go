package substrate

import (
	"bytes"
	"context"
	"fmt"
	"mime/multipart"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Hinkolas/skali/internal/claim"
	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/kubetest"
	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/observe"
	"github.com/Hinkolas/skali/internal/project"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/substrate/seaweed"
	"github.com/Hinkolas/skali/internal/testdb"
)

// writeFilerObject uploads one object through the filer's HTTP API, which
// expects multipart form encoding for file writes.
func writeFilerObject(t *testing.T, client *kube.Client, bucket, name, content string) {
	t.Helper()
	var buffer bytes.Buffer
	form := multipart.NewWriter(&buffer)
	part, err := form.CreateFormFile("file", name)
	require.NoError(t, err)
	_, err = part.Write([]byte(content))
	require.NoError(t, err)
	require.NoError(t, form.Close())

	// ServiceProxyDo cannot set the multipart content type; the filer
	// accepts it via the query-less header-free path only for multipart, so
	// go through a raw proxied request with the boundary header.
	request := client.Clientset.CoreV1().RESTClient().Post().
		Namespace(Namespace).
		Resource("services").
		Name(seaweed.FilerService+":"+fmt.Sprint(seaweed.FilerPort)).
		SubResource("proxy").
		Suffix(seaweed.BucketsPrefix+bucket+"/"+name).
		SetHeader("Content-Type", form.FormDataContentType()).
		Body(buffer.Bytes())
	result := request.Do(context.Background())
	require.NoError(t, result.Error(), "filer upload failed")
}

// TestLiveSeaweedObservation proves the R6 observation exit criterion on a
// real cluster: the poll observer reports store and bucket truth, a
// SeaweedFS outage turns only the seaweedfs source stale (bucket health
// degrades, cluster observation keeps its meaning), and recovery restores
// freshness. Requires TEST_KUBECONFIG and TEST_DATABASE_URL.
func TestLiveSeaweedObservation(t *testing.T) {
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

	observed := observe.NewStore(nil)
	controller := New(Deps{
		DB:       dbSvc,
		Cluster:  KubeCluster{Client: client},
		Observed: observed,
		Seaweed:  seaweed.NewClient(client, Namespace),
	}, Config{Managed: false})

	cleanupPlatform(t, client)

	namespace := kubernetes.RenderNamespace(proj.Name, "production", env.ID.String())
	_, err = client.Apply(ctx, namespace, false)
	require.NoError(t, err)
	t.Cleanup(func() { deleteNamespace(t, client, namespace.Name) })

	// Provision one bucket end to end (the store comes up on the way).
	owner := dbstore.ServiceOwner(proj.ID, env.ID, proj.Name, "production", "files")
	created, err := dbSvc.EnsureBucketClaim(ctx, owner, dbstore.BucketSpec{
		Visibility: "private", StorageQuotaBytes: 1 << 30, Versioning: "disabled",
	})
	require.NoError(t, err)
	deadline := time.Now().Add(10 * time.Minute)
	for {
		require.False(t, time.Now().After(deadline), "bucket not provisioned; last wait: %s",
			controller.WaitingReason(created.ID))
		requeue, err := controller.reconcileBucketClaim(ctx, created.ID)
		_, _ = controller.reconcileObjectStore(ctx)
		if metadata, err := dbSvc.LiveSystemClaim(ctx, MetadataClaimKey); err == nil {
			_, _ = controller.reconcileClaim(ctx, metadata.ID)
		}
		current, getErr := dbSvc.GetBucketClaim(ctx, created.ID)
		require.NoError(t, getErr)
		if claim.Phase(current.Phase) == claim.PhaseProvisioned {
			break
		}
		stepClaim(t, "bucket claim", requeue, err, claim.Phase(current.Phase), controller.WaitingReason(created.ID))
		time.Sleep(2 * time.Second)
	}

	// The cluster watch is simulated fresh; the provider observer runs for
	// real at a tight cadence.
	observed.MarkReady(observe.SourceKubernetes)
	poll := observe.NewPollSource(observed, controller.SeaweedProbe(), observe.PollOptions{
		Source:         seaweed.SourceName,
		Interval:       2 * time.Second,
		StaleThreshold: 8 * time.Second,
	})
	controller.SetProbePoke(poll.Poke)
	pollCtx, cancelPoll := context.WithCancel(ctx)
	defer cancelPoll()
	go func() { _ = poll.Run(pollCtx) }()

	requireEventually(t, time.Minute, func() bool {
		source, ok := observed.SourceNamed(seaweed.SourceName)
		return ok && source.State == module.SourceFresh
	}, "the seaweedfs source never turned fresh")

	// The bucket and store projections join the environment snapshot.
	allocation, err := dbSvc.LiveAllocation(ctx, created.ID)
	require.NoError(t, err)
	requireEventually(t, time.Minute, func() bool {
		resources := observed.Snapshot(env.ID).ForService("buckets.files")
		var bucket *module.BucketStatus
		var storeStatus *module.ObjectStoreStatus
		for _, resource := range resources {
			if resource.Kind == module.KindBucket {
				bucket = resource.Bucket
			}
			if resource.Kind == module.KindObjectStore {
				storeStatus = resource.ObjectStore
			}
		}
		return bucket != nil && bucket.Exists && storeStatus != nil &&
			storeStatus.S3Ready && storeStatus.FilerReady && storeStatus.MastersReady >= 1
	}, "the bucket and store projections never appeared")

	// An object written through the filer shows up as usage on the next
	// polls; the poke shortcut is exercised by the write path in real runs.
	writeFilerObject(t, client, allocation.BucketName, "probe.txt",
		"usage probe payload: twelve dozen bytes of nothing in particular")
	poll.Poke()
	requireEventually(t, 2*time.Minute, func() bool {
		resources := observed.Snapshot(env.ID).ForService("buckets.files")
		for _, resource := range resources {
			if resource.Kind == module.KindBucket && resource.Bucket != nil {
				return resource.Bucket.ObjectCount >= 1 && resource.Bucket.UsedBytes > 0
			}
		}
		return false
	}, "usage never reflected the uploaded object")

	// Outage: scale the store to zero behind the observer's back. Only the
	// seaweedfs source turns stale; the cluster source keeps its meaning,
	// and the bucket module degrades instead of blanking.
	scale := &autoscalingv1.Scale{
		ObjectMeta: metav1.ObjectMeta{Name: seaweed.AllInOneApp, Namespace: Namespace},
		Spec:       autoscalingv1.ScaleSpec{Replicas: 0},
	}
	_, err = client.Clientset.AppsV1().Deployments(Namespace).
		UpdateScale(ctx, seaweed.AllInOneApp, scale, metav1.UpdateOptions{})
	require.NoError(t, err)
	requireEventually(t, 2*time.Minute, func() bool {
		source, ok := observed.SourceNamed(seaweed.SourceName)
		return ok && source.State == module.SourceStale
	}, "the seaweedfs source never turned stale")
	require.Equal(t, module.SourceFresh, observed.Source().State,
		"a seaweed outage must not touch cluster observation")
	resources := observed.Snapshot(env.ID).ForService("buckets.files")
	require.Nil(t, module.StaleSource(resources), "the leading source stays the fresh cluster watch")
	require.Equal(t, module.SourceStale, module.SourceNamed(resources, seaweed.SourceName).State)

	// Recovery: scale back up; the source returns to fresh with the last
	// known objects still present throughout.
	scale.Spec.Replicas = 1
	_, err = client.Clientset.AppsV1().Deployments(Namespace).
		UpdateScale(ctx, seaweed.AllInOneApp, scale, metav1.UpdateOptions{})
	require.NoError(t, err)
	requireEventually(t, 3*time.Minute, func() bool {
		source, ok := observed.SourceNamed(seaweed.SourceName)
		return ok && source.State == module.SourceFresh
	}, "the seaweedfs source never recovered")
}
