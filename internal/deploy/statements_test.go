package deploy

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/artifactstore"
	"github.com/Hinkolas/skali/internal/journal"
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

// recordStatements returns a deploy service and a journal on a pool whose
// statements the returned recorder names; the fixture's own helpers stay
// unrecorded.
func (f *fixture) recordStatements(t *testing.T) (*Service, *journal.Service, *statementRecorder) {
	t.Helper()
	recorder := &statementRecorder{}
	config := f.st.Pool.Config()
	config.ConnConfig.Tracer = recorder
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	st := store.NewStore(pool)
	values, err := valuestore.New(st, strings.Repeat("v", 32))
	require.NoError(t, err)
	return New(st, values, artifactstore.New(st), "test"), journal.NewService(st, "executor-1"), recorder
}

// A deployment starts its run, writes each of its steps, stores its
// revision, and moves its deployment row in one statement each, and
// promotes in one transaction: no run is created and then started, no step
// is ensured and then moved, and nothing is read back that a statement
// could return.
func TestDeploymentWritesOneStatementEach(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	svc, jsvc, recorder := f.recordStatements(t)
	ctx := context.Background()
	definitionVersion := f.submit(t, testManifest, 0)
	candidate, err := f.values.Stage(ctx, f.environmentID,
		map[string]string{"APP_DOMAIN": "demo.example.com", "SESSION_SECRET": "statements-plant-value"})
	require.NoError(t, err)

	recorder.take()
	opened, err := svc.Open(ctx, OpenInput{
		PlanInput: PlanInput{
			EnvironmentID: f.environmentID, DefinitionVersionID: definitionVersion, CandidateID: candidate.ID,
		},
		Actor: "tester", Capabilities: []string{"application"}, RegistryConfigured: true, Journal: jsvc,
	})
	require.NoError(t, err)
	open := recorder.take()
	for _, action := range opened.Actions {
		require.NoError(t, f.artifacts.Verify(ctx, action.ArtifactID, "registry.test/demo/web", testDigest, nil))
	}
	c, err := svc.beginComplete(ctx, opened.Deployment.ID, jsvc)
	require.NoError(t, err)
	defer svc.release(opened.Deployment.ID)
	_, err = svc.finishComplete(ctx, c)
	require.NoError(t, err)
	complete := recorder.take()

	require.Equal(t, 1, open["BeginRun"])
	require.Equal(t, 2, open["RecordStep"], "validate and values")
	require.Equal(t, 1, open["EnsureStep"], "the pending artifacts step")
	require.Equal(t, 5, complete["RecordStep"], "artifacts, then revision and promote twice each")
	require.Equal(t, 1, complete["ListCurrentEnvironmentSecretCiphertexts"], "one redactor")
	require.Equal(t, 1, complete["GetArtifactByID"], "each artifact read once")
	require.Equal(t, 1, complete["StoreRevision"])
	require.Equal(t, 1, complete["MoveDeployment"])
	require.Equal(t, 1, complete["begin"], "the promotion's transaction")
	for _, counts := range []map[string]int{open, complete} {
		for _, name := range []string{"CreateRun", "GetRunForUpdate", "MarkRunRunning", "SetStepStatus",
			"GetStepByRunAndKey", "GetDeploymentForUpdate", "GetRevisionByChecksum", "StampEnvironmentRestart"} {
			require.Zero(t, counts[name], name)
		}
	}
}
