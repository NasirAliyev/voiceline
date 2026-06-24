package domain

import (
	"context"
	"io"
)

// AudioOpener returns a fresh stream over the uploaded audio on each call. The
// transcriber calls it once per HTTP attempt so retries always send a full
// body — a single-use io.Reader would be drained by the first multipart build
// and send nothing on attempt two. The caller is responsible for closing the
// returned ReadCloser.
type AudioOpener func() (io.ReadCloser, error)

// Transcriber turns audio into text. Implemented by adapter/openai; any
// transcription provider fits behind this port.
type Transcriber interface {
	Transcribe(ctx context.Context, open AudioOpener, meta AudioMeta) (Transcript, error)
}

// Analyzer extracts a structured Analysis from a transcript. Implemented by
// adapter/openai; swapping the LLM provider is a one-adapter change.
type Analyzer interface {
	Analyze(ctx context.Context, transcript Transcript) (Analysis, error)
}

// Destination delivers a finished Voiceline to an external system (Google
// Sheets, a webhook, stdout, ...). Adding a destination is a one-adapter change.
type Destination interface {
	Deliver(ctx context.Context, voiceline Voiceline) error
}

// JobStore persists job status and results. The in-memory implementation lives
// in adapter/memstore; a Redis or SQL store would satisfy the same port.
type JobStore interface {
	Create(ctx context.Context, job *Job) error
	Get(ctx context.Context, id string) (*Job, error)
	Update(ctx context.Context, job *Job) error
}
