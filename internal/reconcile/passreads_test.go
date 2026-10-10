package reconcile

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/artifactstore"
	"github.com/Hinkolas/skali/internal/deploy"
	"github.com/Hinkolas/skali/internal/edge/edgeprobe"
	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/valuestore"
)

// statementRecorder names the statements a pool runs by their query name.
type statementRecorder struct {
	mu    sync.Mutex
	names []string
}

func (r *statementRecorder) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	name := strings.TrimSpace(data.SQL)
	if rest, ok := strings.CutPrefix(name, "-- name: "); ok {
		name, _, _ = strings.Cut(rest, " ")
	} else {
		name, _, _ = strings.Cut(name, "\n")
	}
	r.mu.Lock()
	r.names = append(r.names, name)
	r.mu.Unlock()
	return ctx
}

func (r *statementRecorder) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// take counts the statements recorded since the last call, per name.
func (r *statementRecorder) take() map[string]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	counts := map[string]int{}
	for _, name := range r.names {
		counts[name]++
	}
	r.names = nil
	return counts
}

// recordStatements moves the kernel onto a pool whose statements the
// returned recorder names; the fixture's own helpers stay unrecorded.
func (f *kernelFixture) recordStatements(t *testing.T) *statementRecorder {
	t.Helper()
	recorder := &statementRecorder{}
	config := f.st.Pool.Config()
	config.ConnConfig.Tracer = recorder
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	st := store.NewStore(pool)
	values, err := valuestore.New(st, strings.Repeat("k", 32))
	require.NoError(t, err)
	deploySvc := deploy.New(st, values, artifactstore.New(st), "test")
	deploySvc.SetEnqueuer(f.kernel)
	f.kernel.deps.Store = st
	f.kernel.deps.Values = values
	f.kernel.deps.Deploy = deploySvc
	f.kernel.deps.Journal = journal.NewService(st, "kernel-test-boot-1")
	return recorder
}

// A pass reads each input once: the pre-lock probe phase, the render, and
// the redactor share the decoded revision and its pinned values, and a pass
// that journals nothing never builds the redactor. Only the target and the
// running run are read again under the lock, since either may have moved
// while the pass waited for it.
func TestPassReadsEachInputOnce(t *testing.T) {
	t.Parallel()
	f := certFixture(t, Config{RolloutDeadline: time.Hour})
	f.kernel.deps.ProbeDomain = probeResult(nil, edgeprobe.StateReachable, oursAddress)
	recorder := f.recordStatements(t)
	ctx := context.Background()

	f.executeDeploymentManifest(t, certManifest)
	f.fake.SetFresh()
	pass := func() map[string]int {
		recorder.take()
		_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
		require.NoError(t, err)
		return recorder.take()
	}
	once := []string{"GetRevisionByID", "ListPinnedEnvironmentSecretCiphertexts",
		"ListCurrentEnvironmentSecretCiphertexts", "GetEnvironmentByID"}

	rollout := pass()
	f.markHealthy(t)
	f.fake.SetCertificate(f.environmentID, f.namespace, certName, "web",
		module.CertificateStatus{Ready: true, NotAfter: time.Now().Add(60 * 24 * time.Hour)})
	activation := pass()
	require.NotNil(t, f.target(t).ActiveRevisionID)
	for _, counts := range []map[string]int{rollout, activation} {
		require.Equal(t, 1, counts["ListCurrentEnvironmentSecretCiphertexts"], "a journaling pass builds the redactor")
		for _, name := range once {
			require.LessOrEqual(t, counts[name], 1, name)
		}
	}

	converged := pass()
	require.Zero(t, converged["ListCurrentEnvironmentSecretCiphertexts"], "an idle pass builds no redactor")
	for name, n := range converged {
		switch name {
		case "GetEnvironmentTarget", "GetRunningRunByEnvironment":
			require.LessOrEqual(t, n, 2, name)
		default:
			require.Equal(t, 1, n, name)
		}
	}
}
