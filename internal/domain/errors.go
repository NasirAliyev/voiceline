package domain

import "errors"

// Sentinel domain errors. Adapters wrap these with %w at their boundaries so the
// HTTP layer can map a failure to a status code via errors.Is without depending
// on any adapter's concrete error types.
var (
	// ErrJobNotFound is returned when a job id has no record.
	ErrJobNotFound = errors.New("job not found")
	// ErrQueueFull signals the bounded queue is at capacity (backpressure).
	ErrQueueFull = errors.New("processing queue is full")

	// ErrEmptyAudio is returned for a missing or zero-length upload.
	ErrEmptyAudio = errors.New("audio is empty")
	// ErrUnsupportedAudioType is returned when the content type is not allowed.
	ErrUnsupportedAudioType = errors.New("unsupported audio content type")
	// ErrAudioTooLarge is returned when the upload exceeds the configured limit.
	ErrAudioTooLarge = errors.New("audio exceeds maximum allowed size")

	// ErrTranscriptionFailed wraps a failure in the transcription step.
	ErrTranscriptionFailed = errors.New("transcription failed")
	// ErrAnalysisFailed wraps a failure in the analysis/extraction step.
	ErrAnalysisFailed = errors.New("analysis failed")
	// ErrDeliveryFailed wraps a failure delivering to the destination.
	ErrDeliveryFailed = errors.New("delivery failed")

	// ErrInvalidArgument is returned by value-object constructors on bad input.
	ErrInvalidArgument = errors.New("invalid argument")
)
