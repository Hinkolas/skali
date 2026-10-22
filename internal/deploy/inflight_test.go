package deploy

import (
	"context"
	"errors"
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

// disconnectAfter cancels a request once a statement of the named query
// has run, the way a client that hangs up mid-request ends its context.
type disconnectAfter struct {
	query  string
	cancel context.CancelFunc
	once   sync.Once
}

func (d *disconnectAfter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.HasPrefix(strings.TrimSpace(data.SQL), "-- name: "+d.query+" ") {
		return context.WithValue(ctx, d, true)
	}
	return ctx
}

func (d *disconnectAfter) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	if ctx.Value(d) != nil {
		d.once.Do(d.cancel)
	}
}

// A client that disconnects while its deployment opens leaves no run
// running and no window preparing: the cleanup runs on a context of its
// own, so the environment is free for the next deployment at once.
func TestOpenCleansUpAfterTheRequestIsGone(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	definitionVersion := f.submit(t, testManifest, 0)
	candidate := f.stage(t, map[string]string{"APP_DOMAIN": "demo.example.com", "SESSION_SECRET": "disconnect-plant-value"})

	for _, query := range []string{"BeginRun", "CreateDeployment"} {
		t.Run(query, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			config := f.st.Pool.Config()
			config.ConnConfig.Tracer = &disconnectAfter{query: query, cancel: cancel}
			pool, err := pgxpool.NewWithConfig(context.Background(), config)
			require.NoError(t, err)
			defer pool.Close()
			st := store.NewStore(pool)
			values, err := valuestore.New(st, strings.Repeat("v", 32))
			require.NoError(t, err)
			svc := New(st, values, artifactstore.New(st), "test")
			jsvc := journal.NewService(st, "executor-1")

			_, err = svc.Open(ctx, OpenInput{
				PlanInput: PlanInput{
					EnvironmentID: f.environmentID, DefinitionVersionID: definitionVersion, CandidateID: candidate.ID,
				},
				Actor: "tester", Capabilities: []string{"application"}, RegistryConfigured: true, Journal: jsvc,
			})
			require.ErrorIs(t, err, context.Canceled)

			_, err = f.st.GetRunningRunByEnvironment(context.Background(), &f.environmentID)
			require.ErrorIs(t, err, pgx.ErrNoRows, "the opened run was closed")
			_, err = f.st.GetPreparingDeploymentForEnvironment(context.Background(), f.environmentID)
			require.ErrorIs(t, err, pgx.ErrNoRows, "no window stays preparing")
		})
	}
}

// A refused change names the run that holds the environment, both the
// run of an open artifact window and any other running run.
func TestInFlightNamesTheBlockingRun(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	jsvc := journal.NewService(f.st, "executor-1")
	definitionVersion := f.submit(t, testManifest, 0)

	restart, err := jsvc.BeginRun(ctx, journal.RunInput{
		Kind: "restart", ProjectID: f.projectID, EnvironmentID: f.environmentID, Actor: "tester",
	})
	require.NoError(t, err)
	_, err = f.deploy.Open(ctx, OpenInput{
		PlanInput: PlanInput{EnvironmentID: f.environmentID, DefinitionVersionID: definitionVersion,
			CandidateID: f.stage(t, map[string]string{"APP_DOMAIN": "demo.example.com", "SESSION_SECRET": "refused-plant-value"}).ID},
		Actor: "tester", Capabilities: []string{"application"}, RegistryConfigured: true, Journal: jsvc,
	})
	var inFlight *InFlightError
	require.ErrorAs(t, err, &inFlight)
	require.ErrorIs(t, err, ErrDeploymentInFlight)
	require.Equal(t, restart.ID, inFlight.RunID)
	require.Equal(t, "restart", inFlight.Kind)
	require.NoError(t, jsvc.FinishRun(ctx, restart.ID, journal.RunSucceeded))

	window := f.openForComplete(t, jsvc, f.environmentID, definitionVersion, "window-plant-value")
	_, _, err = f.deploy.Teardown(ctx, f.environmentID, false, jsvc, "tester")
	require.True(t, errors.As(err, &inFlight), "got %v", err)
	require.Equal(t, *window.RunID, inFlight.RunID)
	require.Equal(t, "deployment", inFlight.Kind)
}
