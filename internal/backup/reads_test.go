package backup

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/kube"
)

// shortenReadRetries keeps the read retries of a test from waiting out
// the production backoff and grace.
func shortenReadRetries(t *testing.T, grace time.Duration) {
	t.Helper()
	previous := [...]time.Duration{readFailureGrace, readBackoffBase, readBackoffMax, jobPollInterval}
	readFailureGrace, readBackoffBase, readBackoffMax, jobPollInterval = grace, time.Millisecond, 5*time.Millisecond, time.Millisecond
	t.Cleanup(func() {
		readFailureGrace, readBackoffBase, readBackoffMax, jobPollInterval = previous[0], previous[1], previous[2], previous[3]
	})
}

var unavailable = apierrors.NewServiceUnavailable("the server is currently unable to handle the request")

func TestTransientRead(t *testing.T) {
	jobs := schema.GroupResource{Group: "batch", Resource: "jobs"}
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"unavailable", unavailable, true},
		{"throttled", apierrors.NewTooManyRequests("slow down", 1), true},
		{"server timeout", apierrors.NewServerTimeout(jobs, "get", 1), true},
		{"internal", apierrors.NewInternalError(errors.New("etcd")), true},
		{"connection", io.ErrUnexpectedEOF, true},
		{"client timeout", context.DeadlineExceeded, true},
		{"not found", apierrors.NewNotFound(jobs, "j"), false},
		{"forbidden", apierrors.NewForbidden(jobs, "j", errors.New("rbac")), false},
		{"unauthorized", apierrors.NewUnauthorized("token"), false},
		{"invalid", apierrors.NewBadRequest("bad"), false},
		{"no rows", pgx.ErrNoRows, false},
		{"cancelled", context.Canceled, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, transientRead(tc.err))
		})
	}
}

// Failed reads back off with growing pauses and end the wait only after
// the grace, or at once when the wait itself was cancelled.
func TestReadFailuresRetry(t *testing.T) {
	previous := [...]time.Duration{readFailureGrace, readBackoffBase, readBackoffMax}
	t.Cleanup(func() { readFailureGrace, readBackoffBase, readBackoffMax = previous[0], previous[1], previous[2] })
	readFailureGrace, readBackoffBase, readBackoffMax = time.Hour, time.Second, 4*time.Second

	ctx := context.Background()
	var failures readFailures
	var waits []time.Duration
	for range 4 {
		wait, err := failures.retry(ctx, unavailable)
		require.NoError(t, err)
		waits = append(waits, wait)
	}
	require.Equal(t, []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 4 * time.Second}, waits)

	failures.ok()
	wait, err := failures.retry(ctx, unavailable)
	require.NoError(t, err)
	require.Equal(t, time.Second, wait, "a successful read starts the backoff over")

	readFailureGrace = 0
	_, err = failures.retry(ctx, unavailable)
	require.ErrorIs(t, err, unavailable)
	require.ErrorContains(t, err, "reads failed for")

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = (&readFailures{}).retry(cancelled, unavailable)
	require.ErrorIs(t, err, context.Canceled)
}

// jobRun runs one Job through runJob inside a step of a fresh run on a
// fake cluster whose Job reads fail while failing says so, and returns
// what runJob returned.
func jobRun(t *testing.T, failing func(reads int64) error) (error, int64) {
	t.Helper()
	f := newServiceFixture(t)
	ctx := context.Background()
	clientset := fake.NewSimpleClientset()
	f.controller.deps.Kube = &kube.Client{Clientset: clientset}
	var reads atomic.Int64
	clientset.PrependReactor("get", "jobs", func(action k8stesting.Action) (bool, runtime.Object, error) {
		n := reads.Add(1)
		if n == 1 {
			return false, nil, nil // the leftover check before the create
		}
		if err := failing(n - 1); err != nil {
			return true, nil, err
		}
		job, err := clientset.Tracker().Get(batchv1.SchemeGroupVersion.WithResource("jobs"),
			action.GetNamespace(), action.(k8stesting.GetAction).GetName())
		if err != nil {
			return true, nil, err
		}
		finished := job.(*batchv1.Job).DeepCopy()
		finished.Status.Succeeded = 1
		return true, finished, nil
	})

	run, err := f.journal.BeginRun(ctx, journal.RunInput{
		Kind: KindBackup, ProjectID: f.projectID, EnvironmentID: f.environmentID, Actor: "tester",
	})
	require.NoError(t, err)
	scope := journal.NewScope(f.journal, nil, run.ID)
	job := &batchv1.Job{ObjectMeta: jobMeta("skali-backup-test", "ns", run.ID.String())}
	runErr := scope.Step(ctx, "job", "Run job", func(ctx context.Context, log *journal.StepLog) error {
		return f.controller.runJob(ctx, log, job)
	})
	return runErr, reads.Load() - 1
}

// A Job whose status cannot be read for a moment is still watched to its
// end; one whose reads keep failing past the grace, or are refused with an
// answer that will not change, fails the step with the read's error.
func TestRunJobRetriesTransientStatusReads(t *testing.T) {
	t.Run("Transient", func(t *testing.T) {
		shortenReadRetries(t, time.Minute)
		err, reads := jobRun(t, func(n int64) error {
			if n <= 3 {
				return unavailable
			}
			return nil
		})
		require.NoError(t, err)
		require.GreaterOrEqual(t, reads, int64(4), "the watch read on past the failures")
	})

	t.Run("PastTheGrace", func(t *testing.T) {
		shortenReadRetries(t, 30*time.Millisecond)
		err, reads := jobRun(t, func(int64) error { return unavailable })
		require.Error(t, err)
		require.ErrorContains(t, err, "watch job skali-backup-test")
		require.ErrorContains(t, err, "reads failed for")
		require.Greater(t, reads, int64(1))
	})

	t.Run("Verdict", func(t *testing.T) {
		shortenReadRetries(t, time.Minute)
		forbidden := apierrors.NewForbidden(schema.GroupResource{Group: "batch", Resource: "jobs"}, "j", errors.New("rbac"))
		err, reads := jobRun(t, func(int64) error { return forbidden })
		require.ErrorContains(t, err, "forbidden")
		require.EqualValues(t, 1, reads, "a refusal that will not change is not retried")
	})
}

// A restore whose wait for the workloads to stop cannot list them for a
// moment carries on, and completes.
func TestRestoreStopToleratesFailedReads(t *testing.T) {
	shortenReadRetries(t, time.Minute)
	f := newHoldFixture(t)
	clientset, ok := f.controller.deps.Kube.Clientset.(*fake.Clientset)
	require.True(t, ok)
	var lists atomic.Int64
	clientset.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		if lists.Add(1) <= 2 {
			return true, nil, unavailable
		}
		return false, nil, nil
	})
	snapshot := f.snapshot(t, "snap", 3)

	result, row := f.restore(t, snapshot)
	require.Equal(t, StatusSucceeded, row.Status, "error: %v", row.Error)
	require.Equal(t, string(journal.RunSucceeded), f.runStatus(t, result.RunID))
	require.GreaterOrEqual(t, lists.Load(), int64(3), "the wait listed again after the failures")
	target := f.target(t)
	require.Nil(t, target.IncompleteRestoreID)
	require.Equal(t, f.revisionID, *target.ActiveRevisionID)
}
