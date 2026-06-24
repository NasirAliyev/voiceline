package memstore

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/nasiraliev/voiceline/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStoreCRUD(t *testing.T) {
	ctx := context.Background()
	s := New()

	job, err := domain.NewJob("j1", domain.AudioMeta{Filename: "a.m4a"}, time.Now())
	require.NoError(t, err)
	require.NoError(t, s.Create(ctx, job))

	// Duplicate create is rejected.
	require.Error(t, s.Create(ctx, job))

	got, err := s.Get(ctx, "j1")
	require.NoError(t, err)
	assert.Equal(t, domain.StatusQueued, got.Status)

	// Get returns a copy: mutating it must not change stored state.
	got.Status = domain.StatusFailed
	again, err := s.Get(ctx, "j1")
	require.NoError(t, err)
	assert.Equal(t, domain.StatusQueued, again.Status)

	// Update persists a new state.
	job.Complete(domain.Analysis{Title: "T", KeyPoints: []string{"k"}}, time.Now())
	require.NoError(t, s.Update(ctx, job))

	updated, err := s.Get(ctx, "j1")
	require.NoError(t, err)
	assert.Equal(t, domain.StatusCompleted, updated.Status)
	require.NotNil(t, updated.Result)
	assert.Equal(t, "T", updated.Result.Title)

	// Result slices are deep-copied.
	updated.Result.KeyPoints[0] = "mutated"
	fresh, err := s.Get(ctx, "j1")
	require.NoError(t, err)
	assert.Equal(t, "k", fresh.Result.KeyPoints[0])
}

func TestStoreNotFound(t *testing.T) {
	ctx := context.Background()
	s := New()

	_, err := s.Get(ctx, "missing")
	assert.ErrorIs(t, err, domain.ErrJobNotFound)

	err = s.Update(ctx, &domain.Job{ID: "missing"})
	assert.ErrorIs(t, err, domain.ErrJobNotFound)
}

func TestStoreContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := New()
	job, _ := domain.NewJob("j1", domain.AudioMeta{}, time.Now())
	assert.Error(t, s.Create(ctx, job))
}

func TestStoreConcurrentAccess(t *testing.T) {
	ctx := context.Background()
	s := New()
	const n = 50

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("j-%d", i)
			job, _ := domain.NewJob(id, domain.AudioMeta{}, time.Now())
			_ = s.Create(ctx, job)
			job.Start(time.Now())
			_ = s.Update(ctx, job)
			_, _ = s.Get(ctx, id)
		}(i)
	}
	wg.Wait()

	for i := 0; i < n; i++ {
		got, err := s.Get(ctx, fmt.Sprintf("j-%d", i))
		require.NoError(t, err)
		assert.Equal(t, domain.StatusProcessing, got.Status)
	}
}
