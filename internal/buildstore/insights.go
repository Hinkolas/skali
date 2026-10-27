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

// maxStoredInsightsLog bounds a stored log on the way out; redaction may
// lengthen a log past the upload bound, never by this much.
const maxStoredInsightsLog = 4 * build.MaxInsightsLogBytes

// ErrInvalidInsights: the report is malformed or exceeds its bounds.
var ErrInvalidInsights = errors.New("buildstore: invalid insights report")

// PutInsights stores the insights report of the build a step ran,
// replacing an earlier upload. Everything the builder printed passes the
// redactor first; the log is kept compressed.
func (s *Service) PutInsights(ctx context.Context, stepID uuid.UUID, redactor *redact.Redactor, report build.Insights) error {
	if err := report.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidInsights, err)
	}
	steps := make([]build.InsightsStep, len(report.Steps))
	for i, step := range report.Steps {
		step.Name = redactor.Redact(step.Name)
		steps[i] = step
	}
	encodedSteps, err := json.Marshal(steps)
	if err != nil {
		return fmt.Errorf("buildstore: encode insights steps: %w", err)
	}
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := io.WriteString(writer, redactor.Redact(report.Log)); err != nil {
		return fmt.Errorf("buildstore: compress insights log: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("buildstore: compress insights log: %w", err)
	}
	err = s.st.PutBuildInsights(ctx, store.PutBuildInsightsParams{
		StepID:       stepID,
		Outcome:      report.Outcome,
		Engine:       report.Engine,
		Platform:     report.Platform,
		StartedAt:    report.StartedAt,
		FinishedAt:   report.FinishedAt,
		Error:        redactor.Redact(report.Error),
		Steps:        encodedSteps,
		LogGzip:      compressed.Bytes(),
		LogLines:     int32(report.LogLines),
		LogTruncated: report.LogTruncated,
	})
	if err != nil {
		return fmt.Errorf("buildstore: put insights: %w", err)
	}
	return nil
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
	log, err := io.ReadAll(io.LimitReader(reader, maxStoredInsightsLog))
	if err != nil {
		return nil, fmt.Errorf("buildstore: read insights log: %w", err)
	}
	report.Log = string(log)
	return report, nil
}
