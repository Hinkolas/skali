package journal

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Hinkolas/skali/internal/store"
)

// LogEvent is one step-log entry as delivered to readers and subscribers.
// Cursor identity is (AttemptNumber, Seq), which is also the SSE resume id.
type LogEvent struct {
	StepID        uuid.UUID
	AttemptNumber int64
	Seq           int64
	TS            time.Time
	Level         string
	Message       string
	Fields        []byte // JSON object, already redacted
}

// Cursor addresses a position in a step's log across attempts.
type Cursor struct {
	AttemptNumber int64
	Seq           int64
}

func (c Cursor) Before(other Cursor) bool {
	if c.AttemptNumber != other.AttemptNumber {
		return c.AttemptNumber < other.AttemptNumber
	}
	return c.Seq < other.Seq
}

// StepLogs reads one page of a step's log after the cursor.
func (s *Service) StepLogs(ctx context.Context, stepID uuid.UUID, after Cursor, limit int) ([]LogEvent, error) {
	if _, err := s.st.GetStepByID(ctx, stepID); err != nil {
		return nil, notFoundOr(err, "get step")
	}
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	rows, err := s.st.ListStepLogs(ctx, store.ListStepLogsParams{
		StepID:      stepID,
		AfterNumber: after.AttemptNumber,
		AfterSeq:    after.Seq,
		RowLimit:    int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("journal: list step logs: %w", err)
	}
	events := make([]LogEvent, len(rows))
	for i, row := range rows {
		events[i] = LogEvent{
			StepID:        stepID,
			AttemptNumber: row.AttemptNumber,
			Seq:           row.Seq,
			TS:            row.Ts,
			Level:         row.Level,
			Message:       row.Message,
			Fields:        row.Fields,
		}
	}
	return events, nil
}

// Subscription is a live step-log feed: Backlog holds everything after the
// cursor at subscription time, Events delivers entries committed later. The
// channel is closed when the subscriber falls too far behind; clients
// resubscribe from their last cursor.
type Subscription struct {
	Backlog []LogEvent
	Events  <-chan LogEvent
	Cancel  func()
}

// Subscribe registers for live entries first and then reads the backlog, so
// no committed entry is lost in between; an entry can appear in both, and
// readers skip events at or before their cursor.
func (s *Service) Subscribe(ctx context.Context, stepID uuid.UUID, after Cursor) (*Subscription, error) {
	if _, err := s.st.GetStepByID(ctx, stepID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("journal: get step: %w", err)
	}
	events, cancel := s.broadcast.subscribe(stepID)
	backlog, err := s.StepLogs(ctx, stepID, after, 1000)
	if err != nil {
		cancel()
		return nil, err
	}
	return &Subscription{Backlog: backlog, Events: events, Cancel: cancel}, nil
}

// broadcaster is the in-process fan-out for live log entries. Single-daemon
// by design in R1; a multi-replica control plane would replace this with a
// shared channel.
type broadcaster struct {
	mu   sync.Mutex
	subs map[uuid.UUID]map[chan LogEvent]struct{}
}

func newBroadcaster() *broadcaster {
	return &broadcaster{subs: make(map[uuid.UUID]map[chan LogEvent]struct{})}
}

func (b *broadcaster) subscribe(stepID uuid.UUID) (chan LogEvent, func()) {
	ch := make(chan LogEvent, 256)
	b.mu.Lock()
	if b.subs[stepID] == nil {
		b.subs[stepID] = make(map[chan LogEvent]struct{})
	}
	b.subs[stepID][ch] = struct{}{}
	b.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			b.mu.Lock()
			if set, ok := b.subs[stepID]; ok {
				if _, subscribed := set[ch]; subscribed {
					delete(set, ch)
					close(ch)
				}
				if len(set) == 0 {
					delete(b.subs, stepID)
				}
			}
			b.mu.Unlock()
		})
	}
	return ch, cancel
}

// publish delivers to every subscriber of the entry's step. A subscriber
// whose buffer is full is disconnected (channel closed) rather than blocked
// or silently skipped; it resubscribes from its cursor and catches up.
func (b *broadcaster) publish(event LogEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	set := b.subs[event.StepID]
	for ch := range set {
		select {
		case ch <- event:
		default:
			delete(set, ch)
			close(ch)
		}
	}
	if set != nil && len(set) == 0 {
		delete(b.subs, event.StepID)
	}
}
