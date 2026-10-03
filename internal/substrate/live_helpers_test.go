package substrate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
	schedulingv1 "k8s.io/api/scheduling/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/Hinkolas/skali/internal/bundle"
	"github.com/Hinkolas/skali/internal/claim"
	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/layout"
	"github.com/Hinkolas/skali/internal/substrate/cnpg"
	"github.com/Hinkolas/skali/internal/substrate/seaweed"
)

// livePhase uses Go's logger so -v and -json include successful phase timings.
func livePhase(t *testing.T, name string) func() {
	t.Helper()
	start := time.Now()
	t.Logf("phase=%q started", name)
	return func() { t.Helper(); t.Logf("phase=%q elapsed=%s", name, time.Since(start).Round(time.Millisecond)) }
}

var errLiveUnsettled = errors.New("reconcile abandoned unsettled work")

func permanentLiveError(err error) bool {
	if errors.Is(err, errLiveUnsettled) {
		return true
	}
	switch e := err.(type) {
	case *pgconn.PgError:
		return strings.HasPrefix(e.Code, "23") || strings.HasPrefix(e.Code, "42")
	case interface{ Unwrap() []error }:
		for _, child := range e.Unwrap() {
			if permanentLiveError(child) {
				return true
			}
		}
	case interface{ Unwrap() error }:
		return permanentLiveError(e.Unwrap())
	}
	return false
}

// pollLive bounds API calls as well as pauses. Preserve the last observation
// on timeout; SQL schema/constraint errors and abandoned work cannot heal.
func pollLive(ctx context.Context, interval time.Duration, pass func(context.Context) (bool, error)) error {
	var last error
	for {
		if err := ctx.Err(); err != nil {
			return errors.Join(err, last)
		}
		passCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		done, err := pass(passCtx)
		cancel()
		if permanentLiveError(err) {
			return err
		}
		if err == nil && done {
			return nil
		}
		last = err
		select {
		case <-ctx.Done():
			return errors.Join(ctx.Err(), last)
		case <-time.After(interval):
		}
	}
}

func driveLive(t *testing.T, phase string, timeout time.Duration, pass func(context.Context) (bool, error)) {
	t.Helper()
	defer livePhase(t, phase)()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	require.NoError(t, pollLive(ctx, 2*time.Second, pass), phase)
}

func databaseClaimPass(ctx context.Context, c *Controller, db *dbstore.Service, id uuid.UUID, target claim.Phase) (bool, error) {
	requeue, err := c.reconcileClaim(ctx, id)
	if err != nil {
		return false, err
	}
	row, err := db.GetClaim(ctx, id)
	if err != nil {
		return false, err
	}
	phase := claim.Phase(row.Phase)
	if phase == target {
		return true, nil
	}
	if requeue == 0 && phase != claim.PhaseProvisioned && phase != claim.PhaseReleased {
		return false, fmt.Errorf("%w: database phase=%s wait=%q", errLiveUnsettled, phase, c.WaitingReason(id))
	}
	return false, fmt.Errorf("database phase=%s wait=%q", phase, c.WaitingReason(id))
}

func objectStorePass(ctx context.Context, c *Controller, db *dbstore.Service) (bool, error) {
	requeue, err := c.reconcileObjectStore(ctx)
	if permanentLiveError(err) {
		return false, err
	}
	// A transient store error must not starve its metadata dependency.
	metadata, metadataErr := db.LiveSystemClaim(ctx, MetadataClaimKey)
	if metadataErr != nil {
		return false, errors.Join(err, metadataErr)
	}
	done, metadataErr := databaseClaimPass(ctx, c, db, metadata.ID, claim.PhaseProvisioned)
	if err := errors.Join(err, metadataErr); err != nil || !done {
		return false, err
	}
	if requeue != 0 {
		row, err := db.LiveObjectStore(ctx)
		if err != nil {
			return false, err
		}
		_, reason, err := c.objectStoreReady(ctx, *row)
		return false, errors.Join(fmt.Errorf("object store waiting: %s (requeue %s)", reason, requeue), err)
	}
	return true, nil
}

func bucketClaimPass(ctx context.Context, c *Controller, db *dbstore.Service, id uuid.UUID, target claim.Phase, withStore bool) (bool, error) {
	requeue, err := c.reconcileBucketClaim(ctx, id)
	if permanentLiveError(err) {
		return false, err
	}
	if withStore {
		// Always progress dependencies, including when a fresh controller has
		// not yet discovered the existing store's filer target.
		_, storeErr := objectStorePass(ctx, c, db)
		err = errors.Join(err, storeErr)
	}
	if err != nil {
		return false, err
	}
	row, err := db.GetBucketClaim(ctx, id)
	if err != nil {
		return false, err
	}
	phase := claim.Phase(row.Phase)
	if phase == target {
		return true, nil
	}
	if requeue == 0 && phase != claim.PhaseProvisioned && phase != claim.PhaseReleased {
		return false, fmt.Errorf("%w: bucket phase=%s wait=%q", errLiveUnsettled, phase, c.WaitingReason(id))
	}
	return false, fmt.Errorf("bucket phase=%s wait=%q", phase, c.WaitingReason(id))
}

var operatorSetup struct {
	sync.Once
	err error
}

// Infrastructure is lazy and process-scoped, never provisioned by TestMain.
func installOperator(t *testing.T, client *kube.Client) {
	t.Helper()
	operatorSetup.Do(func() {
		defer livePhase(t, "operator setup")()
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
		defer cancel()
		operatorSetup.err = applyLiveOperator(ctx, client)
	})
	require.NoError(t, operatorSetup.err)
}

func applyLiveOperator(ctx context.Context, client *kube.Client) error {
	applier := &bundle.Applier{Client: client}
	if err := applier.ApplyManifest(ctx, bundle.CNPGManifest()); err != nil {
		return err
	}
	if err := applier.WaitDeploymentReady(ctx, "cnpg-system", "cnpg-controller-manager"); err != nil {
		return err
	}
	for name, value := range map[string]int32{
		layout.PriorityClassCritical: layout.PriorityClassCriticalValue,
		layout.PriorityClassHigh:     layout.PriorityClassHighValue,
		layout.PriorityClassNormal:   layout.PriorityClassNormalValue,
	} {
		_, err := client.Clientset.SchedulingV1().PriorityClasses().Create(ctx, &schedulingv1.PriorityClass{ObjectMeta: metav1.ObjectMeta{Name: name}, Value: value}, metav1.CreateOptions{})
		if err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}
	}
	return nil
}

func cleanupPlatform(t *testing.T, client *kube.Client) {
	t.Helper()
	// Register before preflight, so even partially failed cleanup is retried.
	t.Cleanup(func() { deleteNamespace(t, client, Namespace) })
	require.NoError(t, cleanLiveNamespace(client, Namespace), "clean leftover platform")
}

func deleteNamespace(t *testing.T, client *kube.Client, name string) {
	t.Helper()
	defer livePhase(t, "cleanup "+name)()
	if err := cleanLiveNamespace(client, name); err != nil {
		t.Errorf("cleanup %s: %v", name, err)
	}
}

// Each stage gets its own budget so one failure never prevents later cleanup.
// Do not use require/FailNow inside cleanup: preserve the original failure.
type liveCleanupStage struct {
	name    string
	timeout time.Duration
	run     func(context.Context) error
}

func runLiveCleanup(stages []liveCleanupStage) error {
	var errs []error
	for _, stage := range stages {
		ctx, cancel := context.WithTimeout(context.Background(), stage.timeout)
		err := stage.run(ctx)
		cancel()
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", stage.name, err))
		}
	}
	return errors.Join(errs...)
}

func cleanLiveNamespace(client *kube.Client, name string) error {
	if name == Namespace {
		client.ForgetServiceAddress(name, seaweed.S3Service, seaweed.S3Port)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	_, err := client.Clientset.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
	cancel()
	if apierrors.IsNotFound(err) {
		return nil
	}
	// Even a failed initial read must not prevent the deletion attempt.
	var stages []liveCleanupStage
	if name == Namespace {
		selector := seaweed.SystemLabel + "=object-storage"
		stages = append(stages,
			liveCleanupStage{"remove SeaweedFS controllers", time.Minute, func(ctx context.Context) error {
				var errs []error
				for _, gvr := range []schema.GroupVersionResource{deploymentsGVR, statefulSetsGVR} {
					if err := deleteLiveCollection(ctx, client, gvr, name, selector); err != nil {
						errs = append(errs, err)
					}
				}
				return errors.Join(errs...)
			}},
			liveCleanupStage{"drain SeaweedFS pods", 2 * time.Minute, func(ctx context.Context) error { return waitLivePods(ctx, client, name, selector) }},
			liveCleanupStage{"remove CNPG clusters", time.Minute, func(ctx context.Context) error { return deleteLiveCollection(ctx, client, cnpg.ClusterGVR, name, "") }},
			liveCleanupStage{"drain platform pods", 3 * time.Minute, func(ctx context.Context) error { return waitLivePods(ctx, client, name, "") }},
		)
	}
	stages = append(stages, liveCleanupStage{"delete namespace", time.Minute, func(ctx context.Context) error {
		err := client.Clientset.CoreV1().Namespaces().Delete(ctx, name, metav1.DeleteOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}}, liveCleanupStage{"wait for namespace removal", 3 * time.Minute, func(ctx context.Context) error {
		err := pollLive(ctx, time.Second, func(ctx context.Context) (bool, error) {
			_, err := client.Clientset.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return true, nil
			}
			return false, err
		})
		if err == nil {
			return nil
		}
		return fmt.Errorf("%w; remaining: %s", err, liveNamespaceDiagnostic(client, name))
	}})
	return runLiveCleanup(stages)
}

func deleteLiveCollection(ctx context.Context, client *kube.Client, gvr schema.GroupVersionResource, namespace, selector string) error {
	foreground := metav1.DeletePropagationForeground
	err := client.Dynamic.Resource(gvr).Namespace(namespace).DeleteCollection(ctx, metav1.DeleteOptions{PropagationPolicy: &foreground}, metav1.ListOptions{LabelSelector: selector})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

func waitLivePods(ctx context.Context, client *kube.Client, namespace, selector string) error {
	return pollLive(ctx, time.Second, func(ctx context.Context) (bool, error) {
		pods, err := client.Clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		if len(pods.Items) == 0 {
			return true, nil
		}
		var states []string
		for _, p := range pods.Items {
			states = append(states, fmt.Sprintf("%s phase=%s deleting=%v finalizers=%v", p.Name, p.Status.Phase, p.DeletionTimestamp, p.Finalizers))
		}
		return false, fmt.Errorf("pods remaining: %s", strings.Join(states, "; "))
	})
}

func liveNamespaceDiagnostic(client *kube.Client, name string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var details []string
	ns, err := client.Clientset.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		details = append(details, fmt.Sprintf("namespace finalizers=%v conditions=%v", ns.Spec.Finalizers, ns.Status.Conditions))
	} else {
		details = append(details, err.Error())
	}
	for _, gvr := range []schema.GroupVersionResource{{Version: "v1", Resource: "pods"}, {Version: "v1", Resource: "persistentvolumeclaims"}, cnpg.ClusterGVR, cnpg.DatabaseGVR, deploymentsGVR, statefulSetsGVR} {
		objects, err := client.Dynamic.Resource(gvr).Namespace(name).List(ctx, metav1.ListOptions{})
		if err != nil {
			details = append(details, fmt.Sprintf("%s: %v", gvr.Resource, err))
			continue
		}
		for _, obj := range objects.Items {
			details = append(details, fmt.Sprintf("%s/%s finalizers=%v deleting=%v", gvr.Resource, obj.GetName(), obj.GetFinalizers(), obj.GetDeletionTimestamp()))
		}
	}
	return strings.Join(details, "; ")
}
