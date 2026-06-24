package app

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/nasiraliev/voiceline/internal/domain"
)

// errPoolClosed is returned by Submit after the pool has begun shutting down.
var errPoolClosed = errors.New("worker pool is shut down")

// Task is the unit of work handed to the pipeline: a job id, a way to (re)open
// the audio, and its metadata. It deliberately carries no context — the worker
// supplies a fresh, detached one — so request cancellation never aborts async
// processing.
type Task struct {
	JobID string
	Open  domain.AudioOpener
	Meta  domain.AudioMeta
	// Cleanup, if set, runs after processing (success or failure) to release
	// resources such as the spooled temp file. Keeps the app layer free of any
	// file/path detail — it just invokes the callback.
	Cleanup func()
}

// Handler processes a single task. *Processor satisfies it.
type Handler interface {
	Process(ctx context.Context, task Task) error
}

// Pool is a bounded worker pool with backpressure. Submit is non-blocking and
// returns domain.ErrQueueFull when the buffer is full. Workers run until
// Shutdown drains the queue (or its context expires).
type Pool struct {
	handler     Handler
	workers     int
	queue       chan Task
	procTimeout time.Duration
	logger      *slog.Logger

	// baseCtx roots every per-task context; cancelling it (on shutdown) aborts
	// all in-flight work. It is independent of any HTTP request context.
	baseCtx    context.Context
	baseCancel context.CancelFunc

	mu     sync.RWMutex // guards closed; also serializes Submit against close(queue)
	closed bool
	wg     sync.WaitGroup
}

// NewPool constructs a pool. workers and queueSize must be >= 1 (validated in
// config). logger may be nil (defaults to slog.Default()).
func NewPool(handler Handler, workers, queueSize int, procTimeout time.Duration, logger *slog.Logger) *Pool {
	if logger == nil {
		logger = slog.Default()
	}
	baseCtx, baseCancel := context.WithCancel(context.Background())
	return &Pool{
		handler:     handler,
		workers:     workers,
		queue:       make(chan Task, queueSize),
		procTimeout: procTimeout,
		logger:      logger,
		baseCtx:     baseCtx,
		baseCancel:  baseCancel,
	}
}

// Start launches the worker goroutines. Each worker runs until the queue is
// closed and drained by Shutdown.
func (p *Pool) Start() {
	for i := 0; i < p.workers; i++ {
		p.wg.Add(1)
		go p.worker()
	}
}

// Submit enqueues a task without blocking. It returns domain.ErrQueueFull when
// the buffer is full (backpressure -> the caller responds 503) and errPoolClosed
// after shutdown has begun.
func (p *Pool) Submit(task Task) error {
	// RLock lets concurrent submits proceed while still being mutually exclusive
	// with Shutdown's Lock, so we never send on a closed channel.
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.closed {
		return errPoolClosed
	}
	select {
	case p.queue <- task:
		return nil
	default:
		return domain.ErrQueueFull
	}
}

// Shutdown stops accepting new tasks, then waits for in-flight and queued tasks
// to drain. If ctx expires first, it cancels all task contexts and waits for the
// workers to unwind (handlers must honor ctx), returning ctx.Err().
func (p *Pool) Shutdown(ctx context.Context) error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	close(p.queue)
	p.mu.Unlock()

	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		p.baseCancel()
		return nil
	case <-ctx.Done():
		// Grace period exceeded: cancel in-flight task contexts, then wait for
		// the (well-behaved) workers to exit so we don't leak goroutines.
		p.baseCancel()
		<-done
		return ctx.Err()
	}
}

// worker consumes tasks until the queue is closed and drained.
func (p *Pool) worker() {
	defer p.wg.Done()
	for task := range p.queue {
		p.runTask(task)
	}
}

// runTask builds the detached, timeout-bounded processing context (rooted at the
// pool's base context, NOT any request) and invokes the handler, logging the
// outcome once.
func (p *Pool) runTask(task Task) {
	if task.Cleanup != nil {
		defer task.Cleanup()
	}

	ctx, cancel := context.WithTimeout(p.baseCtx, p.procTimeout)
	defer cancel()

	start := time.Now()
	if err := p.handler.Process(ctx, task); err != nil {
		p.logger.ErrorContext(ctx, "job processing failed",
			slog.String("job_id", task.JobID),
			slog.Duration("elapsed", time.Since(start)),
			slog.Any("error", err),
		)
		return
	}
	p.logger.InfoContext(ctx, "job processed",
		slog.String("job_id", task.JobID),
		slog.Duration("elapsed", time.Since(start)),
	)
}
