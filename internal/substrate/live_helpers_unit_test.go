package substrate

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	kt "k8s.io/client-go/testing"

	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/substrate/cnpg"
	"github.com/Hinkolas/skali/internal/substrate/seaweed"
)

func TestBucketFixtureRecordsRequireReleaseAndPreserveStore(t *testing.T) {
	ctx := context.Background()
	fx := newRotationFixture(t)
	f := &bucketLiveFixture{pool: fx.pool, db: fx.db}
	projectID := *fx.claim.ProjectID
	require.ErrorContains(t, f.removeProjectRecords(projectID), "live bucket records")
	_, err := fx.db.GetBucketClaim(ctx, fx.claim.ID)
	require.NoError(t, err, "refusing cleanup must preserve scenario evidence")
	_, err = fx.db.ReleaseBucketClaim(ctx, fx.claim.ID)
	require.NoError(t, err)
	require.NoError(t, fx.db.CompleteBucketClaimRelease(ctx, fx.claim.ID))
	require.NoError(t, f.removeProjectRecords(projectID))
	_, err = fx.db.GetBucketClaim(ctx, fx.claim.ID)
	require.ErrorIs(t, err, dbstore.ErrNotFound)
	var projects, environments, allocations int
	require.NoError(t, fx.pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM projects), (SELECT count(*) FROM environments),
		(SELECT count(*) FROM bucket_allocations)`).Scan(&projects, &environments, &allocations))
	require.Zero(t, projects)
	require.Zero(t, environments)
	require.Zero(t, allocations)
	row, err := fx.db.LiveObjectStore(ctx)
	require.NoError(t, err)
	require.Equal(t, fx.allocation.StoreID, row.ID)
	require.NoError(t, f.removeProjectRecords(projectID), "repeated record cleanup is harmless")
}

func TestProvisioningDriverErrorHandling(t *testing.T) {
	// These are unit tests despite the subject: no TestLive prefix, so the
	// live selector never counts harness tests as integration coverage.
	for _, code := range []string{"23502", "23505", "42P01", "42703"} {
		t.Run(code, func(t *testing.T) {
			calls := 0
			err := pollLive(context.Background(), time.Millisecond, func(context.Context) (bool, error) {
				calls++
				return false, fmt.Errorf("wrapped: %w", &pgconn.PgError{Code: code})
			})
			require.Error(t, err)
			require.Equal(t, 1, calls)
		})
	}
	require.True(t, permanentLiveError(errors.Join(
		&pgconn.PgError{Code: "40001"}, fmt.Errorf("metadata: %w", &pgconn.PgError{Code: "42P01"}))),
		"a transient primary error must not hide a permanent dependency error")
	calls := 0
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, pollLive(ctx, time.Millisecond, func(context.Context) (bool, error) {
		calls++
		if calls < 3 {
			return false, &pgconn.PgError{Code: "40001"}
		}
		return true, nil
	}))
	require.Equal(t, 3, calls)
	ctx2, cancel2 := context.WithCancel(context.Background())
	err := pollLive(ctx2, time.Millisecond, func(ctx context.Context) (bool, error) { cancel2(); return false, errors.New("waiting for metadata") })
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorContains(t, err, "waiting for metadata")
	calls = 0
	err = pollLive(context.Background(), time.Millisecond, func(context.Context) (bool, error) {
		calls++
		return false, fmt.Errorf("%w: pending", errLiveUnsettled)
	})
	require.ErrorIs(t, err, errLiveUnsettled)
	require.Equal(t, 1, calls)
}

func TestCleanupAttemptsLaterStagesAfterFailure(t *testing.T) {
	var stages []string
	original := errors.New("failed to remove controller")
	err := runLiveCleanup([]liveCleanupStage{
		{"remove", time.Second, func(context.Context) error { stages = append(stages, "remove"); return original }},
		{"drain", time.Millisecond, func(ctx context.Context) error { stages = append(stages, "drain"); <-ctx.Done(); return ctx.Err() }},
		{"namespace", time.Second, func(ctx context.Context) error { stages = append(stages, "namespace"); return ctx.Err() }},
	})
	require.ErrorIs(t, err, original)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, []string{"remove", "drain", "namespace"}, stages)
}

func TestPlatformCleanupOrderAndPartialState(t *testing.T) {
	for _, state := range []string{"absent", "partial", "running", "terminating"} {
		t.Run(state, func(t *testing.T) {
			var objects []runtime.Object
			if state != "absent" {
				ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: Namespace}}
				if state == "terminating" {
					now := metav1.Now()
					ns.DeletionTimestamp = &now
					ns.Spec.Finalizers = []corev1.FinalizerName{corev1.FinalizerKubernetes}
				}
				objects = append(objects, ns, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "seaweed", Namespace: Namespace, Labels: map[string]string{seaweed.SystemLabel: "object-storage"}}})
				if state != "partial" {
					objects = append(objects, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pg", Namespace: Namespace}})
				}
			}
			typed := kubefake.NewClientset(objects...)
			dynamic := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
			client := &kube.Client{Clientset: typed, Dynamic: dynamic}
			var order []string
			dynamic.PrependReactor("delete-collection", "*", func(action kt.Action) (bool, runtime.Object, error) {
				resource := action.GetResource().Resource
				order = append(order, resource)
				opts := action.(kt.DeleteCollectionActionImpl).GetDeleteOptions()
				require.NotNil(t, opts.PropagationPolicy)
				require.Equal(t, metav1.DeletePropagationForeground, *opts.PropagationPolicy)
				switch resource {
				case "deployments":
					require.Equal(t, seaweed.SystemLabel+"=object-storage", action.(kt.DeleteCollectionAction).GetListRestrictions().Labels.String())
					_ = typed.CoreV1().Pods(Namespace).Delete(context.Background(), "seaweed", metav1.DeleteOptions{})
				case "clusters":
					_, err := typed.CoreV1().Pods(Namespace).Get(context.Background(), "seaweed", metav1.GetOptions{})
					require.True(t, apierrors.IsNotFound(err))
					_ = typed.CoreV1().Pods(Namespace).Delete(context.Background(), "pg", metav1.DeleteOptions{})
					if state == "partial" {
						return true, nil, apierrors.NewNotFound(cnpg.ClusterGVR.GroupResource(), "")
					}
				}
				return true, nil, nil
			})
			typed.PrependReactor("delete", "namespaces", func(action kt.Action) (bool, runtime.Object, error) {
				order = append(order, "namespace")
				objects, err := typed.Tracker().List(corev1.SchemeGroupVersion.WithResource("pods"), corev1.SchemeGroupVersion.WithKind("Pod"), Namespace)
				require.NoError(t, err)
				require.Empty(t, objects.(*corev1.PodList).Items)
				return false, nil, nil
			})
			require.NoError(t, cleanLiveNamespace(client, Namespace))
			if state == "absent" {
				require.Empty(t, order)
			} else {
				require.Equal(t, []string{"deployments", "statefulsets", "clusters", "namespace"}, order)
			}
			require.NoError(t, cleanLiveNamespace(client, Namespace), "repeated cleanup is a no-op")
		})
	}
}

func TestPlatformCleanupStillDeletesNamespaceAfterControllerError(t *testing.T) {
	typed := kubefake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: Namespace}})
	dynamic := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	dynamic.PrependReactor("delete-collection", "*", func(action kt.Action) (bool, runtime.Object, error) {
		if action.GetResource().Resource == "deployments" {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "deployments"}, "", errors.New("denied"))
		}
		return true, nil, nil
	})
	err := cleanLiveNamespace(&kube.Client{Clientset: typed, Dynamic: dynamic}, Namespace)
	require.ErrorContains(t, err, "denied")
	_, err = typed.CoreV1().Namespaces().Get(context.Background(), Namespace, metav1.GetOptions{})
	require.True(t, apierrors.IsNotFound(err))
}
