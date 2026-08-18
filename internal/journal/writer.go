package journal

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/Hinkolas/skali/internal/redact"
	"github.com/Hinkolas/skali/internal/store"
)

// Retention caps. Package constants for now; promoted to configuration if a
// deployment ever needs different limits.
const (
	// MaxEntriesPerAttempt bounds one attempt's log; the final slot holds a
	// single truncation marker and later appends are dropped.
	MaxEntriesPerAttempt = 2000
	// MaxEntryBytes bounds one message; longer messages are cut with a
	// truncation suffix.
	MaxEntryBytes = 8192
	// MaxRunsPerEnvironment keeps the newest terminal runs per environment.
	MaxRunsPerEnvironment = 50
	// MaxRunAge drops terminal runs older than this everywhere.
	MaxRunAge = 90 * 24 * time.Hour
)

func maxRunAgeCutoff() time.Time { return time.Now().Add(-MaxRunAge) }

const truncationSuffix = "... [truncated]"

// Append writes one redacted, bounded log entry. It locks the attempt row:
// appends to terminal attempts are rejected (append-only on live attempts),
// the sequence is gap-free, and the entry cap inserts one warn marker at the
// final slot then drops everything after it.
func (s *Service) Append(ctx context.Context, attemptID uuid.UUID, redactor *redact.Redactor, level, message string, fields map[string]any) error {
	if !validLevel(level) {
		return fmt.Errorf("journal: invalid log level %q", level)
	}
	message = redactor.Redact(message)
	if len(message) > MaxEntryBytes {
		message = message[:MaxEntryBytes-len(truncationSuffix)] + truncationSuffix
	}
	encodedFields, err := encodeFields(redactor, fields)
	if err != nil {
		return err
	}

	var event *LogEvent
	err = s.st.WithTx(ctx, func(q *store.Queries) error {
		attempt, err := q.GetAttemptForUpdate(ctx, attemptID)
		if err != nil {
			return notFoundOr(err, "lock attempt")
		}
		if Attempts.Terminal(AttemptStatus(attempt.Status)) {
			return ErrAttemptTerminal
		}
		maxSeq, err := q.GetMaxRunLogSeq(ctx, attemptID)
		if err != nil {
			return fmt.Errorf("journal: max seq: %w", err)
		}
		if maxSeq >= MaxEntriesPerAttempt {
			return nil // capped: drop silently, the marker is already there
		}
		entryLevel, entryMessage, entryFields := level, message, encodedFields
		if maxSeq == MaxEntriesPerAttempt-1 {
			entryLevel = "warn"
			entryMessage = "log truncated: entry cap reached"
			entryFields = []byte("{}")
		}
		id, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("journal: generate id: %w", err)
		}
		row, err := q.AppendRunLog(ctx, store.AppendRunLogParams{
			ID:        id,
			AttemptID: attemptID,
			Seq:       maxSeq + 1,
			Level:     entryLevel,
			Message:   entryMessage,
			Fields:    entryFields,
		})
		if err != nil {
			return fmt.Errorf("journal: append: %w", err)
		}
		event = &LogEvent{
			StepID:        attempt.StepID,
			AttemptNumber: attempt.Number,
			Seq:           row.Seq,
			TS:            row.Ts,
			Level:         row.Level,
			Message:       row.Message,
			Fields:        row.Fields,
		}
		return nil
	})
	if err != nil {
		return err
	}
	// Publish only after the transaction committed, so subscribers never
	// see an entry that was rolled back.
	if event != nil {
		s.broadcast.Publish(event.StepID, *event)
	}
	return nil
}

// encodeFields redacts every string field value and encodes the map.
func encodeFields(redactor *redact.Redactor, fields map[string]any) ([]byte, error) {
	if len(fields) == 0 {
		return []byte("{}"), nil
	}
	cleaned := make(map[string]any, len(fields))
	for key, value := range fields {
		if text, ok := value.(string); ok {
			cleaned[key] = redactor.Redact(text)
			continue
		}
		cleaned[key] = value
	}
	encoded, err := json.Marshal(cleaned)
	if err != nil {
		return nil, fmt.Errorf("journal: encode fields: %w", err)
	}
	return encoded, nil
}

func validLevel(level string) bool {
	switch level {
	case "debug", "info", "warn", "error":
		return true
	}
	return false
}

// Writer binds an attempt and a redactor for ergonomic step logging.
type Writer struct {
	svc       *Service
	attemptID uuid.UUID
	redactor  *redact.Redactor
}

func (s *Service) Writer(attemptID uuid.UUID, redactor *redact.Redactor) *Writer {
	return &Writer{svc: s, attemptID: attemptID, redactor: redactor}
}

func (w *Writer) Log(ctx context.Context, level, message string, fields map[string]any) error {
	if w == nil {
		return nil
	}
	return w.svc.Append(ctx, w.attemptID, w.redactor, level, message, fields)
}

func (w *Writer) Debug(ctx context.Context, message string) error {
	return w.Log(ctx, "debug", message, nil)
}

func (w *Writer) Info(ctx context.Context, message string) error {
	return w.Log(ctx, "info", message, nil)
}

func (w *Writer) Warn(ctx context.Context, message string) error {
	return w.Log(ctx, "warn", message, nil)
}

func (w *Writer) Error(ctx context.Context, message string) error {
	return w.Log(ctx, "error", message, nil)
}
