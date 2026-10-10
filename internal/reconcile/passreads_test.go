package reconcile

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
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
	r.record(data.SQL)
	return ctx
}

func (r *statementRecorder) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// A batch's statements are recorded one by one, like single ones.
func (r *statementRecorder) TraceBatchStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceBatchStartData) context.Context {
	for _, query := range data.Batch.QueuedQueries {
		r.record(query.SQL)
	}
	return ctx
}

func (r *statementRecorder) TraceBatchQuery(context.Context, *pgx.Conn, pgx.TraceBatchQueryData) {}

func (r *statementRecorder) TraceBatchEnd(context.Context, *pgx.Conn, pgx.TraceBatchEndData) {}

func (r *statementRecorder) record(sql string) {
	name := strings.TrimSpace(sql)
	if rest, ok := strings.CutPrefix(name, "-- name: "); ok {
		name, _, _ = strings.Cut(rest, " ")
	} else {
		name, _, _ = strings.Cut(name, "\n")
	}
	r.mu.Lock()
	r.names = append(r.names, name)
	r.mu.Unlock()
}

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
// that journals nothing never builds the redactor. Only the environment,
// its target, its running run, its intercepts, restart stamps, and whether
// it holds hostname claims are read again under the lock, in one
// statement, since any of them may have moved while the pass waited for it.
// The revision row itself is read once per target, not once per pass.
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
		"ListCurrentEnvironmentSecretCiphertexts", "ListStepStates"}

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
	require.Zero(t, converged["GetRevisionByID"], "the target's row is kept from an earlier pass")
	for name, n := range converged {
		if name == "GetEnvironmentPass" {
			require.Equal(t, 2, n, name)
			continue
		}
		require.Equal(t, 1, n, name)
	}
}

// A pass reads its run's steps once and writes each step it touches in one
// statement: no step is ensured, read back, or appended to line by line, a
// waiting pass whose reasons did not change writes nothing, and the
// activation finishes the run in one statement.
func TestPassJournalsOneStatementPerStep(t *testing.T) {
	t.Parallel()
	f := certFixture(t, Config{RolloutDeadline: time.Hour})
	f.kernel.deps.ProbeDomain = probeResult(nil, edgeprobe.StateReachable, oursAddress)
	recorder := f.recordStatements(t)
	ctx := context.Background()

	f.executeDeploymentManifest(t, certManifest)
	f.fake.SetFresh()
	f.fake.SetCertificate(f.environmentID, f.namespace, certName, "web",
		module.CertificateStatus{Ready: true, NotAfter: time.Now().Add(60 * 24 * time.Hour)})
	pass := func() map[string]int {
		recorder.take()
		_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
		require.NoError(t, err)
		return recorder.take()
	}

	rollout := pass()
	waiting := pass()
	f.markHealthy(t)
	activation := pass()
	require.NotNil(t, f.target(t).ActiveRevisionID)

	for _, counts := range []map[string]int{rollout, waiting, activation} {
		require.Equal(t, 1, counts["ListStepStates"])
		for _, name := range []string{"EnsureStep", "GetStepByRunAndKey", "LatestStepLog",
			"CreateAttempt", "AppendRunLog", "FinishAttempt", "begin"} {
			require.Zero(t, counts[name], name)
		}
	}
	require.Positive(t, rollout["RecordStep"])
	require.Zero(t, waiting["RecordStep"], "an unchanged wait writes nothing")
	require.Equal(t, 1, activation["FinishRun"])
}

// The pass row carries every intercept and restart stamp, and the pass
// decodes them as the old per-table reads did: host ports per application,
// and stamps as UTC RFC3339 whatever the session's time zone.
func TestPassRowCarriesInterceptsAndRestarts(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{})
	ctx := context.Background()
	state, err := f.st.GetEnvironmentPass(ctx, f.environmentID)
	require.NoError(t, err)
	intercepts, err := passIntercepts(state)
	require.NoError(t, err)
	restarts, err := passRestarts(state)
	require.NoError(t, err)
	require.Nil(t, intercepts)
	require.Nil(t, restarts)
	require.False(t, state.ClaimsHostnames)

	require.NoError(t, f.st.InsertEnvironmentIntercept(ctx, store.InsertEnvironmentInterceptParams{
		EnvironmentID: f.environmentID, ApplicationKey: "web", Ports: []byte(`{"http": 3000}`),
	}))
	for _, key := range []string{"web", "worker"} {
		_, err := f.st.StampApplicationRestart(ctx, store.StampApplicationRestartParams{
			EnvironmentID: f.environmentID, ApplicationKey: key,
		})
		require.NoError(t, err)
	}
	stamped, err := f.st.ListEnvironmentRestarts(ctx, f.environmentID)
	require.NoError(t, err)
	want := map[string]string{}
	for _, row := range stamped {
		want[row.ApplicationKey] = row.RestartedAt.UTC().Format(time.RFC3339)
	}

	tx, err := f.st.Pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, "SET LOCAL TimeZone = 'Asia/Kolkata'")
	require.NoError(t, err)
	state, err = store.New(tx).GetEnvironmentPass(ctx, f.environmentID)
	require.NoError(t, err)
	intercepts, err = passIntercepts(state)
	require.NoError(t, err)
	require.Equal(t, map[string]map[string]int32{"web": {"http": 3000}}, intercepts)
	restarts, err = passRestarts(state)
	require.NoError(t, err)
	require.Equal(t, want, restarts)
}

// A pass asks the cluster for live route hosts only when the environment
// holds a hostname claim, and then releases the retired claims whose
// router is gone.
func TestPassReleasesRetiredHostnamesOnlyWithClaims(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{})
	ctx := context.Background()
	asked := 0
	f.kernel.deps.LiveRouteHosts = func(context.Context, uuid.UUID) (map[string]bool, error) {
		asked++
		return map[string]bool{}, nil
	}
	f.executeDeployment(t)
	f.fake.SetFresh()
	_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Zero(t, asked, "an environment without claims has nothing to release")

	_, err = f.st.Pool.Exec(ctx, `INSERT INTO hostname_claims (hostname, environment_id) VALUES ('gone.example.com', $1)`,
		f.environmentID)
	require.NoError(t, err)
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, 1, asked)
	_, err = f.st.GetHostnameClaim(ctx, "gone.example.com")
	require.ErrorIs(t, err, pgx.ErrNoRows, "the retired claim whose router is gone is released")
}
