package buildstore

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Hinkolas/skali/internal/build"
	"github.com/Hinkolas/skali/internal/redact"
	"github.com/Hinkolas/skali/internal/store"
)

// ErrInvalidInsights: the report is malformed or exceeds its bounds.
var ErrInvalidInsights = errors.New("buildstore: invalid insights report")

// PutInsights stores the insights report of the build a step ran,
// replacing an earlier upload. The report is sealed first: everything the
// builder printed passes the redactor before anything is cut to its
// stored bound. The log is kept compressed. guard runs in the storing
// transaction after the write; its error rolls the report back, so no
// reader ever sees a report it refused.
func (s *Service) PutInsights(ctx context.Context, stepID uuid.UUID, redactor *redact.Redactor, report build.Insights,
	guard func(q *store.Queries) error) error {
	if err := report.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidInsights, err)
	}
	report = report.Seal(redactor.Redact)
	encodedSteps, err := json.Marshal(report.Steps)
	if err != nil {
		return fmt.Errorf("buildstore: encode insights steps: %w", err)
	}
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := io.WriteString(writer, report.Log); err != nil {
		return fmt.Errorf("buildstore: compress insights log: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("buildstore: compress insights log: %w", err)
	}
	return s.st.WithTx(ctx, func(q *store.Queries) error {
		if err := q.PutBuildInsights(ctx, store.PutBuildInsightsParams{
			StepID:       stepID,
			Outcome:      report.Outcome,
			Engine:       report.Engine,
			Platform:     report.Platform,
			StartedAt:    report.StartedAt,
			FinishedAt:   report.FinishedAt,
			Error:        report.Error,
			Steps:        encodedSteps,
			LogGzip:      compressed.Bytes(),
			LogLines:     int32(report.LogLines),
			LogTruncated: report.LogTruncated,
		}); err != nil {
			return fmt.Errorf("buildstore: put insights: %w", err)
		}
		if guard == nil {
			return nil
		}
		return guard(q)
	})
}

// GetInsights reads the insights report of a step's build; ErrNotFound
// when none was uploaded.
func (s *Service) GetInsights(ctx context.Context, stepID uuid.UUID) (*build.Insights, error) {
	row, err := s.st.GetBuildInsights(ctx, stepID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("buildstore: get insights: %w", err)
	}
	report := &build.Insights{
		Outcome:      row.Outcome,
		Engine:       row.Engine,
		Platform:     row.Platform,
		StartedAt:    row.StartedAt,
		FinishedAt:   row.FinishedAt,
		Error:        row.Error,
		LogLines:     int(row.LogLines),
		LogTruncated: row.LogTruncated,
	}
	if err := json.Unmarshal(row.Steps, &report.Steps); err != nil {
		return nil, fmt.Errorf("buildstore: decode insights steps: %w", err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(row.LogGzip))
	if err != nil {
		return nil, fmt.Errorf("buildstore: open insights log: %w", err)
	}
	log, err := io.ReadAll(io.LimitReader(reader, build.MaxStoredInsightsLogBytes+1))
	if err != nil {
		return nil, fmt.Errorf("buildstore: read insights log: %w", err)
	}
	if len(log) > build.MaxStoredInsightsLogBytes {
		return nil, fmt.Errorf("buildstore: insights log of step %s exceeds its stored bound", stepID)
	}
	report.Log = string(log)
	return report, nil
}
