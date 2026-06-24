// Package app holds the application/use-case layer: it orchestrates the domain
// ports (transcribe -> analyze -> deliver) and runs them on a bounded worker
// pool. It depends only on domain, never on transport or adapters.
package app

import (
	"context"
	"fmt"
	"time"

	"github.com/nasiraliev/voiceline/internal/domain"
)

// Clock returns the current time. Injected so tests can pin timestamps.
type Clock func() time.Time

// Processor runs a single job through the pipeline, advancing its status in the
// store at each step. It contains no transport, provider, or storage detail —
// only domain ports — so swapping the LLM provider or destination changes
// nothing here.
type Processor struct {
	transcriber domain.Transcriber
	analyzer    domain.Analyzer
	destination domain.Destination
	store       domain.JobStore
	now         Clock
}

// NewProcessor wires the pipeline ports. now may be nil (defaults to time.Now).
func NewProcessor(
	transcriber domain.Transcriber,
	analyzer domain.Analyzer,
	destination domain.Destination,
	store domain.JobStore,
	now Clock,
) *Processor {
	if now == nil {
		now = time.Now
	}
	return &Processor{
		transcriber: transcriber,
		analyzer:    analyzer,
		destination: destination,
		store:       store,
		now:         now,
	}
}

// Process runs one task to completion, persisting status transitions as it goes.
// It returns nil on success and a wrapped error on failure (the job is also
// marked failed in the store). The ctx MUST be the worker's detached,
// timeout-bounded context — never the inbound HTTP request context, which is
// already cancelled by the time async processing runs.
func (p *Processor) Process(ctx context.Context, task Task) error {
	job, err := p.store.Get(ctx, task.JobID)
	if err != nil {
		return fmt.Errorf("load job %s: %w", task.JobID, err)
	}

	job.Start(p.now())
	if err := p.store.Update(ctx, job); err != nil {
		return fmt.Errorf("mark job %s processing: %w", task.JobID, err)
	}

	transcript, err := p.transcriber.Transcribe(ctx, task.Open, task.Meta)
	if err != nil {
		return p.fail(ctx, job, domain.ErrTranscriptionFailed, err)
	}

	analysis, err := p.analyzer.Analyze(ctx, transcript)
	if err != nil {
		return p.fail(ctx, job, domain.ErrAnalysisFailed, err)
	}
	// Carry the verbatim transcript into the canonical note so downstream sinks
	// (and the status endpoint) have the full context, not just the summary.
	analysis.Transcript = transcript.Text

	voiceline := domain.Voiceline{
		JobID:          job.ID,
		CreatedAt:      job.CreatedAt,
		SourceFilename: task.Meta.Filename,
		Analysis:       analysis,
	}
	if err := p.destination.Deliver(ctx, voiceline); err != nil {
		return p.fail(ctx, job, domain.ErrDeliveryFailed, err)
	}

	job.Complete(analysis, p.now())
	if err := p.store.Update(ctx, job); err != nil {
		return fmt.Errorf("mark job %s completed: %w", task.JobID, err)
	}
	return nil
}

// fail records a sanitized failure reason on the job — the stage sentinel text
// only, never the raw provider error, which may carry sensitive detail — and
// returns the wrapped error (sentinel + cause) for the worker to log server-side.
func (p *Processor) fail(ctx context.Context, job *domain.Job, stage, cause error) error {
	job.Fail(stage.Error(), p.now())
	if err := p.store.Update(ctx, job); err != nil {
		return fmt.Errorf("%w: %w (and failed to persist job state: %v)", stage, cause, err)
	}
	return fmt.Errorf("%w: %w", stage, cause)
}
