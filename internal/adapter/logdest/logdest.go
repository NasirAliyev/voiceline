// Package logdest provides a domain.Destination that logs the delivered note.
// It is the zero-setup default so the whole pipeline runs end-to-end with only
// an OpenAI key — no Google account required.
package logdest

import (
	"context"
	"log/slog"

	"github.com/nasiraliev/voiceline/internal/domain"
)

// Destination logs each delivered Voiceline via structured logging.
type Destination struct {
	logger *slog.Logger
}

// New builds a log destination. logger may be nil (defaults to slog.Default()).
func New(logger *slog.Logger) *Destination {
	if logger == nil {
		logger = slog.Default()
	}
	return &Destination{logger: logger}
}

// Deliver logs the note's identifiers and summary. The full transcript is
// deliberately omitted to avoid dumping large, potentially sensitive text at
// info level (logging hygiene).
func (d *Destination) Deliver(ctx context.Context, v domain.Voiceline) error {
	d.logger.InfoContext(ctx, "voiceline delivered (log sink)",
		slog.String("job_id", v.JobID),
		slog.String("source", v.SourceFilename),
		slog.String("title", v.Analysis.Title),
		slog.String("summary", v.Analysis.Summary),
		slog.Int("key_points", len(v.Analysis.KeyPoints)),
		slog.Int("action_items", len(v.Analysis.ActionItems)),
	)
	return nil
}
