package workstats

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DBTracer charges each statement's round trip, and each wait for a pooled
// connection, to the pass in the statement's context; a batch is one round
// trip. Install it as the pool's ConnConfig.Tracer; a statement outside a
// pass costs one context lookup.
type DBTracer struct{}

var (
	_ pgx.QueryTracer       = DBTracer{}
	_ pgx.BatchTracer       = DBTracer{}
	_ pgxpool.AcquireTracer = DBTracer{}
)

type queryStarted struct{}

func (DBTracer) TraceBatchStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceBatchStartData) context.Context {
	if PassFrom(ctx) == nil {
		return ctx
	}
	return context.WithValue(ctx, queryStarted{}, time.Now())
}

func (DBTracer) TraceBatchQuery(context.Context, *pgx.Conn, pgx.TraceBatchQueryData) {}

func (tracer DBTracer) TraceBatchEnd(ctx context.Context, conn *pgx.Conn, _ pgx.TraceBatchEndData) {
	tracer.TraceQueryEnd(ctx, conn, pgx.TraceQueryEndData{})
}

type acquireStarted struct{}

func (DBTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	if PassFrom(ctx) == nil {
		return ctx
	}
	return context.WithValue(ctx, queryStarted{}, time.Now())
}

func (DBTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	if started, ok := ctx.Value(queryStarted{}).(time.Time); ok {
		took := time.Since(started)
		PassFrom(ctx).charge(func(c *Cost) {
			c.DBQueries++
			c.DB += took
		})
	}
}

func (DBTracer) TraceAcquireStart(ctx context.Context, _ *pgxpool.Pool, _ pgxpool.TraceAcquireStartData) context.Context {
	if PassFrom(ctx) == nil {
		return ctx
	}
	return context.WithValue(ctx, acquireStarted{}, time.Now())
}

func (DBTracer) TraceAcquireEnd(ctx context.Context, _ *pgxpool.Pool, _ pgxpool.TraceAcquireEndData) {
	if started, ok := ctx.Value(acquireStarted{}).(time.Time); ok {
		took := time.Since(started)
		PassFrom(ctx).charge(func(c *Cost) { c.DBAcquire += took })
	}
}
