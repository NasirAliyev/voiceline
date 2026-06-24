// Package memstore provides an in-memory domain.JobStore. It is the service's
// single-instance, non-persistent state.
//
// NOTE: completed jobs are never evicted, so the map grows for the lifetime of
// the process. That is an accepted trade-off for this single-instance exercise;
// a horizontally-scaled deployment would implement domain.JobStore over Redis or
// SQL (same port, zero core changes) and add TTL/eviction.
package memstore

import (
	"context"
	"fmt"
	"sync"

	"github.com/nasiraliev/voiceline/internal/domain"
)

// Store is a concurrency-safe in-memory job store. Reads and writes are guarded
// by an RWMutex and every boundary copies the job, so callers can never mutate
// stored state without going through Update.
type Store struct {
	mu   sync.RWMutex
	jobs map[string]*domain.Job
}

// New returns an empty store.
func New() *Store {
	return &Store{jobs: make(map[string]*domain.Job)}
}

// Create inserts a new job. It errors if the id already exists.
func (s *Store) Create(ctx context.Context, job *domain.Job) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.jobs[job.ID]; exists {
		return fmt.Errorf("job %s already exists", job.ID)
	}
	s.jobs[job.ID] = cloneJob(job)
	return nil
}

// Get returns a copy of the job, or domain.ErrJobNotFound.
func (s *Store) Get(ctx context.Context, id string) (*domain.Job, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	job, exists := s.jobs[id]
	if !exists {
		return nil, fmt.Errorf("%w: %s", domain.ErrJobNotFound, id)
	}
	return cloneJob(job), nil
}

// Update overwrites an existing job. It errors with domain.ErrJobNotFound if the
// job was never created.
func (s *Store) Update(ctx context.Context, job *domain.Job) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.jobs[job.ID]; !exists {
		return fmt.Errorf("%w: %s", domain.ErrJobNotFound, job.ID)
	}
	s.jobs[job.ID] = cloneJob(job)
	return nil
}

// cloneJob deep-copies a job so the stored value is fully detached from any
// caller-held reference (including the Result pointer and its slices).
func cloneJob(j *domain.Job) *domain.Job {
	clone := *j
	if j.Result != nil {
		result := *j.Result
		result.KeyPoints = append([]string(nil), j.Result.KeyPoints...)
		result.ActionItems = append([]domain.ActionItem(nil), j.Result.ActionItems...)
		clone.Result = &result
	}
	return &clone
}
