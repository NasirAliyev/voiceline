package domain

import (
	"fmt"
	"strings"
	"time"
)

// Status is the lifecycle state of a Job.
type Status string

// Job lifecycle states.
const (
	StatusQueued     Status = "queued"
	StatusProcessing Status = "processing"
	StatusCompleted  Status = "completed"
	StatusFailed     Status = "failed"
)

// Job is the aggregate root tracking one recording through the pipeline. Its
// state transitions are driven only through the methods below so the invariants
// (timestamps advance, a sanitized reason accompanies failure) hold in one place.
type Job struct {
	ID        string
	Status    Status
	Audio     AudioMeta
	CreatedAt time.Time
	UpdatedAt time.Time
	Result    *Analysis
	Err       string // sanitized, client-safe failure reason; empty unless failed
}

// NewJob creates a queued Job for the given id and audio metadata.
func NewJob(id string, audio AudioMeta, now time.Time) (*Job, error) {
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("%w: job id is required", ErrInvalidArgument)
	}
	return &Job{
		ID:        id,
		Status:    StatusQueued,
		Audio:     audio,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

// Start marks the job as processing.
func (j *Job) Start(now time.Time) {
	j.Status = StatusProcessing
	j.UpdatedAt = now
}

// Complete stores a copy of the analysis result and marks the job completed.
func (j *Job) Complete(result Analysis, now time.Time) {
	r := result
	j.Result = &r
	j.Status = StatusCompleted
	j.Err = ""
	j.UpdatedAt = now
}

// Fail records a sanitized reason and marks the job failed.
func (j *Job) Fail(reason string, now time.Time) {
	j.Status = StatusFailed
	j.Err = reason
	j.UpdatedAt = now
}
