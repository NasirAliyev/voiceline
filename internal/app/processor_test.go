package app

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/nasiraliev/voiceline/internal/adapter/memstore"
	"github.com/nasiraliev/voiceline/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- hand-written port stubs -------------------------------------------------

type stubTranscriber struct {
	transcript domain.Transcript
	err        error
	calls      int
}

func (s *stubTranscriber) Transcribe(_ context.Context, _ domain.AudioOpener, _ domain.AudioMeta) (domain.Transcript, error) {
	s.calls++
	return s.transcript, s.err
}

type stubAnalyzer struct {
	analysis domain.Analysis
	err      error
	calls    int
}

func (s *stubAnalyzer) Analyze(_ context.Context, _ domain.Transcript) (domain.Analysis, error) {
	s.calls++
	return s.analysis, s.err
}

type stubDestination struct {
	delivered []domain.Voiceline
	err       error
}

func (s *stubDestination) Deliver(_ context.Context, v domain.Voiceline) error {
	if s.err != nil {
		return s.err
	}
	s.delivered = append(s.delivered, v)
	return nil
}

// --- helpers -----------------------------------------------------------------

func fixedClock() Clock {
	return func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
}

func openerFrom(s string) domain.AudioOpener {
	return func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(s)), nil
	}
}

func seedJob(t *testing.T, store *memstore.Store) (string, domain.AudioMeta) {
	t.Helper()
	meta := domain.AudioMeta{Filename: "call.m4a", ContentType: "audio/m4a", Size: 99}
	job, err := domain.NewJob("job-1", meta, time.Unix(1_600_000_000, 0).UTC())
	require.NoError(t, err)
	require.NoError(t, store.Create(context.Background(), job))
	return job.ID, meta
}

// --- tests -------------------------------------------------------------------

func TestProcessorHappyPath(t *testing.T) {
	store := memstore.New()
	id, meta := seedJob(t, store)

	transcript := domain.Transcript{Text: "we met Acme today", Language: "en"}
	analysis := domain.Analysis{
		Title:       "Acme sync",
		Summary:     "Discussed renewal",
		KeyPoints:   []string{"renewal in Q3"},
		ActionItems: []domain.ActionItem{{Task: "Send quote", Owner: "Sam", Due: "Friday"}},
	}
	tr := &stubTranscriber{transcript: transcript}
	an := &stubAnalyzer{analysis: analysis}
	dest := &stubDestination{}

	p := NewProcessor(tr, an, dest, store, fixedClock())
	err := p.Process(context.Background(), Task{JobID: id, Open: openerFrom("audio"), Meta: meta})
	require.NoError(t, err)

	got, err := store.Get(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusCompleted, got.Status)
	assert.Empty(t, got.Err)
	require.NotNil(t, got.Result)
	assert.Equal(t, "Acme sync", got.Result.Title)
	// Transcript is carried into the canonical note.
	assert.Equal(t, transcript.Text, got.Result.Transcript)

	// Destination received exactly one fully-mapped Voiceline.
	require.Len(t, dest.delivered, 1)
	delivered := dest.delivered[0]
	assert.Equal(t, id, delivered.JobID)
	assert.Equal(t, meta.Filename, delivered.SourceFilename)
	assert.Equal(t, "Acme sync", delivered.Analysis.Title)
	assert.Equal(t, transcript.Text, delivered.Analysis.Transcript)
	require.Len(t, delivered.Analysis.ActionItems, 1)
	assert.Equal(t, "Send quote", delivered.Analysis.ActionItems[0].Task)
}

func TestProcessorFailurePaths(t *testing.T) {
	tests := []struct {
		name          string
		tr            *stubTranscriber
		an            *stubAnalyzer
		dest          *stubDestination
		wantStage     error
		wantDelivered int
	}{
		{
			name:      "transcription fails",
			tr:        &stubTranscriber{err: errors.New("whisper http 500 boom")},
			an:        &stubAnalyzer{},
			dest:      &stubDestination{},
			wantStage: domain.ErrTranscriptionFailed,
		},
		{
			name:      "analysis fails",
			tr:        &stubTranscriber{transcript: domain.Transcript{Text: "t"}},
			an:        &stubAnalyzer{err: errors.New("gpt http 429 rate limited")},
			dest:      &stubDestination{},
			wantStage: domain.ErrAnalysisFailed,
		},
		{
			name:      "delivery fails",
			tr:        &stubTranscriber{transcript: domain.Transcript{Text: "t"}},
			an:        &stubAnalyzer{analysis: domain.Analysis{Title: "x"}},
			dest:      &stubDestination{err: errors.New("sheets http 403 forbidden")},
			wantStage: domain.ErrDeliveryFailed,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := memstore.New()
			id, meta := seedJob(t, store)
			p := NewProcessor(tt.tr, tt.an, tt.dest, store, fixedClock())

			err := p.Process(context.Background(), Task{JobID: id, Open: openerFrom("x"), Meta: meta})
			require.Error(t, err)
			assert.ErrorIs(t, err, tt.wantStage)

			got, gerr := store.Get(context.Background(), id)
			require.NoError(t, gerr)
			assert.Equal(t, domain.StatusFailed, got.Status)
			// Job carries only the sanitized stage text — never the raw cause.
			assert.Equal(t, tt.wantStage.Error(), got.Err)
			assert.Len(t, tt.dest.delivered, tt.wantDelivered)
		})
	}
}

func TestProcessorMissingJob(t *testing.T) {
	store := memstore.New()
	p := NewProcessor(&stubTranscriber{}, &stubAnalyzer{}, &stubDestination{}, store, fixedClock())

	err := p.Process(context.Background(), Task{JobID: "nonexistent", Open: openerFrom("x")})
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrJobNotFound)
}
