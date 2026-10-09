package store

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Hinkolas/skali/internal/workstats"
)

// LockEnvironment serializes target transitions and cluster mutations across
// processes. A dedicated connection avoids exhausting the query pool while
// several workers hold locks. Closing it always releases the session lock.
// A pass in ctx is charged the connection setup and the wait apart from its
// statements.
func (s *Store) LockEnvironment(ctx context.Context, id uuid.UUID) (func(), error) {
	config := s.Pool.Config().ConnConfig.Copy()
	config.Tracer = nil
	started := time.Now()
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return nil, err
	}
	connected := time.Now()
	sum := sha256.Sum256(append([]byte("skali-environment:"), id[:]...))
	key := int64(binary.BigEndian.Uint64(sum[:8]))
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", key); err != nil {
		conn.Close(context.Background())
		return nil, err
	}
	workstats.PassFrom(ctx).AddLock(connected.Sub(started), time.Since(connected))
	return func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		conn.Close(cleanup)
	}, nil
}
