package domain

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewJob(t *testing.T) {
	now := time.Now()
	meta := AudioMeta{Filename: "note.m4a", ContentType: "audio/m4a", Size: 1234}

	t.Run("valid starts queued", func(t *testing.T) {
		j, err := NewJob("job-1", meta, now)
		require.NoError(t, err)
		assert.Equal(t, StatusQueued, j.Status)
		assert.Equal(t, meta, j.Audio)
		assert.Equal(t, now, j.CreatedAt)
		assert.Equal(t, now, j.UpdatedAt)
		assert.Nil(t, j.Result)
		assert.Empty(t, j.Err)
	})

	t.Run("blank id rejected", func(t *testing.T) {
		_, err := NewJob("   ", meta, now)
		assert.ErrorIs(t, err, ErrInvalidArgument)
	})
}

func TestJobTransitions(t *testing.T) {
	start := time.Now()
	j, err := NewJob("job-1", AudioMeta{}, start)
	require.NoError(t, err)

	processedAt := start.Add(time.Second)
	j.Start(processedAt)
	assert.Equal(t, StatusProcessing, j.Status)
	assert.Equal(t, processedAt, j.UpdatedAt)

	completedAt := start.Add(2 * time.Second)
	result := Analysis{Title: "Title", Summary: "Summary"}
	j.Complete(result, completedAt)
	assert.Equal(t, StatusCompleted, j.Status)
	require.NotNil(t, j.Result)
	assert.Equal(t, "Title", j.Result.Title)
	assert.Empty(t, j.Err)
	assert.Equal(t, completedAt, j.UpdatedAt)

	// Complete must store a copy so later mutation of the caller's value cannot
	// reach into the stored job.
	result.Title = "mutated"
	assert.Equal(t, "Title", j.Result.Title)
}

func TestJobFail(t *testing.T) {
	now := time.Now()
	j, err := NewJob("job-1", AudioMeta{}, now)
	require.NoError(t, err)

	j.Start(now)
	failedAt := now.Add(time.Second)
	j.Fail("transcription failed", failedAt)

	assert.Equal(t, StatusFailed, j.Status)
	assert.Equal(t, "transcription failed", j.Err)
	assert.Equal(t, failedAt, j.UpdatedAt)
	assert.Nil(t, j.Result)
}
