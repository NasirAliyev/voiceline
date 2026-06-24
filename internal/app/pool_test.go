package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/nasiraliev/voiceline/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeHandler records calls and can optionally block in Process until released,
// to exercise backpressure, concurrency limits, and shutdown.
type fakeHandler struct {
	mu            sync.Mutex
	processed     int
	curConcurrent int
	maxConcurrent int
	sawDeadline   bool

	started chan struct{} // signalled (non-blocking) when a task enters Process
	release chan struct{} // if non-nil, Process blocks until closed or ctx done
}

func (h *fakeHandler) Process(ctx context.Context, _ Task) error {
	h.mu.Lock()
	h.curConcurrent++
	if h.curConcurrent > h.maxConcurrent {
		h.maxConcurrent = h.curConcurrent
	}
	if _, ok := ctx.Deadline(); ok {
		h.sawDeadline = true
	}
	h.mu.Unlock()

	if h.started != nil {
		select {
		case h.started <- struct{}{}:
		default:
		}
	}

	var err error
	if h.release != nil {
		select {
		case <-h.release:
		case <-ctx.Done():
			err = ctx.Err()
		}
	}

	h.mu.Lock()
	h.curConcurrent--
	if err == nil {
		h.processed++
	}
	h.mu.Unlock()
	return err
}

func (h *fakeHandler) counts() (processed, maxConcurrent int, sawDeadline bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.processed, h.maxConcurrent, h.sawDeadline
}

func TestPoolProcessesTasks(t *testing.T) {
	h := &fakeHandler{}
	p := NewPool(h, 2, 8, time.Second, testLogger())
	p.Start()

	const n = 5
	for i := 0; i < n; i++ {
		require.NoError(t, p.Submit(Task{JobID: fmt.Sprintf("j-%d", i)}))
	}
	require.NoError(t, p.Shutdown(context.Background()))

	processed, _, sawDeadline := h.counts()
	assert.Equal(t, n, processed)
	assert.True(t, sawDeadline, "worker must pass a deadline-bounded context")
}

func TestPoolBackpressure(t *testing.T) {
	h := &fakeHandler{started: make(chan struct{}, 8), release: make(chan struct{})}
	// One worker, buffer of one: saturate both, then expect ErrQueueFull.
	p := NewPool(h, 1, 1, time.Second, testLogger())
	p.Start()

	require.NoError(t, p.Submit(Task{JobID: "a"})) // taken by the worker, blocks
	<-h.started
	require.NoError(t, p.Submit(Task{JobID: "b"})) // fills the buffer

	err := p.Submit(Task{JobID: "c"}) // worker busy + buffer full
	assert.ErrorIs(t, err, domain.ErrQueueFull)

	close(h.release)
	require.NoError(t, p.Shutdown(context.Background()))
}

func TestPoolGracefulDrain(t *testing.T) {
	h := &fakeHandler{}
	p := NewPool(h, 3, 16, time.Second, testLogger())
	p.Start()

	const n = 10
	for i := 0; i < n; i++ {
		require.NoError(t, p.Submit(Task{JobID: fmt.Sprintf("j-%d", i)}))
	}

	// Shutdown blocks until every queued task has been processed.
	require.NoError(t, p.Shutdown(context.Background()))
	processed, _, _ := h.counts()
	assert.Equal(t, n, processed)

	// Submitting after shutdown is rejected, and not as backpressure.
	err := p.Submit(Task{JobID: "late"})
	require.Error(t, err)
	assert.NotErrorIs(t, err, domain.ErrQueueFull)
}

func TestPoolShutdownTimeoutCancelsInFlight(t *testing.T) {
	h := &fakeHandler{started: make(chan struct{}, 1), release: make(chan struct{})}
	p := NewPool(h, 1, 1, time.Second, testLogger())
	p.Start()

	require.NoError(t, p.Submit(Task{JobID: "stuck"}))
	<-h.started // worker is now blocked inside Process

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := p.Shutdown(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)

	close(h.release) // harmless: the worker already unwound via ctx cancellation
}

func TestPoolRespectsWorkerLimit(t *testing.T) {
	const workers = 3
	h := &fakeHandler{started: make(chan struct{}, 32), release: make(chan struct{})}
	p := NewPool(h, workers, 32, time.Second, testLogger())
	p.Start()

	const n = 12
	for i := 0; i < n; i++ {
		require.NoError(t, p.Submit(Task{JobID: fmt.Sprintf("j-%d", i)}))
	}

	// Wait for `workers` tasks to be concurrently in-flight.
	for i := 0; i < workers; i++ {
		<-h.started
	}
	time.Sleep(20 * time.Millisecond) // let any (incorrect) extra worker surface

	_, maxConcurrent, _ := h.counts()
	assert.Equal(t, workers, maxConcurrent, "exactly the worker limit should run at once")

	close(h.release)
	require.NoError(t, p.Shutdown(context.Background()))
	processed, _, _ := h.counts()
	assert.Equal(t, n, processed)
}
