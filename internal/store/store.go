// Store-level glue around the sqlc-generated Queries: pool construction and
// transaction handling. sqlc owns the other files in this package.
package store

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Hinkolas/skali/internal/workstats"
)

// NewPool connects a pgx pool and verifies the database is reachable.
func NewPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("store: parse DATABASE_URL: %w", err)
	}
	// Background passes are charged their round trips (see workstats).
	cfg.ConnConfig.Tracer = workstats.DBTracer{}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("store: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}
	return pool, nil
}

// Store bundles the pool with the generated query API.
type Store struct {
	Pool *pgxpool.Pool
	*Queries

	locksOnce sync.Once
	locks     *pgxpool.Pool // see lockPool
	locksErr  error
}

// Close closes the environment lock connections. The query pool belongs to
// the caller that opened it.
func (s *Store) Close() {
	s.locksOnce.Do(func() { s.locksErr = errors.New("store: closed") })
	if s.locks != nil {
		s.locks.Close()
	}
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{Pool: pool, Queries: New(pool)}
}

// WithTx runs fn inside a transaction, committing when it returns nil.
func (s *Store) WithTx(ctx context.Context, fn func(q *Queries) error) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit

	if err := fn(s.Queries.WithTx(tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: commit: %w", err)
	}
	return nil
}
