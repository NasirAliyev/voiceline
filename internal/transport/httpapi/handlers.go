package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/nasiraliev/voiceline/internal/app"
	"github.com/nasiraliev/voiceline/internal/domain"
)

// Route paths and form/field constants.
const (
	RouteSubmit = "/api/v1/voicelines"
	RouteStatus = "/api/v1/voicelines/:id"
	RouteHealth = "/healthz"

	statusURLPrefix = "/api/v1/voicelines/"
	formFieldAudio  = "audio"
	tempFilePattern = "voiceline-*"
	retryAfterValue = "5"

	// maxMultipartMemory bounds how much of a multipart upload Gin buffers in
	// memory before spilling to its own temp files; the hard size limit is
	// enforced separately via http.MaxBytesReader.
	maxMultipartMemory = 8 << 20 // 8 MiB
	headerContentType  = "Content-Type"
)

// submitter is the subset of the worker pool the handler depends on.
type submitter interface {
	Submit(task app.Task) error
}

// Handlers serves the HTTP endpoints.
type Handlers struct {
	store        domain.JobStore
	pool         submitter
	logger       *slog.Logger
	maxBytes     int64
	allowedTypes map[string]bool
	spoolDir     string
	now          func() time.Time
}

// NewHandlers wires the handler dependencies. spoolDir is where uploads are
// streamed to disk ("" uses the OS temp dir). logger may be nil.
func NewHandlers(store domain.JobStore, pool submitter, logger *slog.Logger, maxBytes int64, allowedTypes []string, spoolDir string) *Handlers {
	if logger == nil {
		logger = slog.Default()
	}
	allowed := make(map[string]bool, len(allowedTypes))
	for _, t := range allowedTypes {
		allowed[strings.ToLower(strings.TrimSpace(t))] = true
	}
	return &Handlers{
		store:        store,
		pool:         pool,
		logger:       logger,
		maxBytes:     maxBytes,
		allowedTypes: allowed,
		spoolDir:     spoolDir,
		now:          time.Now,
	}
}

// Health is a liveness probe.
func (h *Handlers) Health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// Submit accepts an audio upload, validates it, spools it to disk, creates a
// queued job, and enqueues it for async processing. It returns 202 immediately.
func (h *Handlers) Submit(c *gin.Context) {
	ctx := c.Request.Context()

	// Enforce the size cap at the HTTP layer (defense in depth alongside the
	// per-file check below).
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, h.maxBytes)

	fileHeader, err := c.FormFile(formFieldAudio)
	if err != nil {
		switch {
		case errors.Is(err, http.ErrMissingFile):
			respondError(c, http.StatusBadRequest, "missing audio file field")
		case isTooLarge(err):
			respondError(c, http.StatusRequestEntityTooLarge, "audio exceeds maximum allowed size")
		default:
			respondError(c, http.StatusBadRequest, "invalid multipart form")
		}
		return
	}

	if fileHeader.Size == 0 {
		respondError(c, http.StatusBadRequest, "audio file is empty")
		return
	}
	if fileHeader.Size > h.maxBytes {
		respondError(c, http.StatusRequestEntityTooLarge, "audio exceeds maximum allowed size")
		return
	}

	contentType := detectContentType(fileHeader)
	if !h.allowedTypes[contentType] {
		respondError(c, http.StatusUnsupportedMediaType, fmt.Sprintf("unsupported audio content type: %q", contentType))
		return
	}

	// Stream the upload to a temp file so we never hold the whole audio in
	// memory and the worker can re-open it for transcription retries.
	tempPath, err := h.spool(fileHeader)
	if err != nil {
		h.logger.ErrorContext(ctx, "failed to spool upload", slog.Any("error", err))
		respondError(c, http.StatusInternalServerError, "failed to store upload")
		return
	}

	jobID := uuid.NewString()
	meta := domain.AudioMeta{Filename: fileHeader.Filename, ContentType: contentType, Size: fileHeader.Size}
	job, err := domain.NewJob(jobID, meta, h.now())
	if err != nil {
		removeTemp(tempPath)
		respondError(c, http.StatusInternalServerError, "failed to create job")
		return
	}

	// The job must exist before the worker can pick it up, so create then submit.
	if err := h.store.Create(ctx, job); err != nil {
		removeTemp(tempPath)
		h.logger.ErrorContext(ctx, "failed to create job", slog.Any("error", err))
		respondError(c, http.StatusInternalServerError, "failed to create job")
		return
	}

	task := app.Task{
		JobID:   jobID,
		Open:    fileOpener(tempPath),
		Meta:    meta,
		Cleanup: func() { removeTemp(tempPath) }, // worker removes the temp file when done
	}
	if err := h.pool.Submit(task); err != nil {
		// Never entered the pool: clean up the temp file and mark the job failed.
		removeTemp(tempPath)
		h.failQueued(ctx, job)
		if errors.Is(err, domain.ErrQueueFull) {
			c.Header(headerRetryAfter, retryAfterValue)
			respondError(c, http.StatusServiceUnavailable, "server is busy, please retry later")
			return
		}
		respondError(c, http.StatusServiceUnavailable, "unable to enqueue job")
		return
	}

	c.JSON(http.StatusAccepted, submitResponse{
		ID:        jobID,
		Status:    string(domain.StatusQueued),
		StatusURL: statusURLPrefix + jobID,
	})
}

// Status returns a job's current status and result.
func (h *Handlers) Status(c *gin.Context) {
	job, err := h.store.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		if errors.Is(err, domain.ErrJobNotFound) {
			respondError(c, http.StatusNotFound, "job not found")
			return
		}
		h.logger.ErrorContext(c.Request.Context(), "failed to load job", slog.Any("error", err))
		respondError(c, http.StatusInternalServerError, "failed to load job")
		return
	}
	c.JSON(http.StatusOK, newStatusResponse(job))
}

// failQueued marks a job failed when it could not be enqueued, keeping store
// state consistent (the client never receives this job's id).
func (h *Handlers) failQueued(ctx context.Context, job *domain.Job) {
	job.Fail("could not enqueue job", h.now())
	if err := h.store.Update(ctx, job); err != nil {
		h.logger.ErrorContext(ctx, "failed to mark unqueued job failed", slog.Any("error", err))
	}
}

// spool streams the uploaded file to a temp file and returns its path.
func (h *Handlers) spool(fh *multipart.FileHeader) (string, error) {
	src, err := fh.Open()
	if err != nil {
		return "", fmt.Errorf("open upload: %w", err)
	}
	defer src.Close()

	tmp, err := os.CreateTemp(h.spoolDir, tempFilePattern)
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	// Bound the copy so a mismatched Content-Length cannot exceed the cap.
	if _, err := io.Copy(tmp, io.LimitReader(src, h.maxBytes)); err != nil {
		_ = tmp.Close()
		removeTemp(tmp.Name())
		return "", fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		removeTemp(tmp.Name())
		return "", fmt.Errorf("close temp file: %w", err)
	}
	return tmp.Name(), nil
}

// fileOpener returns an AudioOpener that yields a fresh handle to the temp file
// on each call (so transcription retries always read from the start).
func fileOpener(path string) domain.AudioOpener {
	return func() (io.ReadCloser, error) {
		return os.Open(path)
	}
}

func removeTemp(path string) {
	if path == "" {
		return
	}
	_ = os.Remove(path)
}

// detectContentType returns the normalized MIME type declared on the upload part.
func detectContentType(fh *multipart.FileHeader) string {
	ct := fh.Header.Get(headerContentType)
	if ct == "" {
		return ""
	}
	if mediaType, _, err := mime.ParseMediaType(ct); err == nil {
		return strings.ToLower(mediaType)
	}
	return strings.ToLower(strings.TrimSpace(ct))
}

// isTooLarge reports whether err is the http.MaxBytesReader limit being exceeded.
func isTooLarge(err error) bool {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		return true
	}
	// Fallback for cases where the multipart parser does not preserve the type.
	return strings.Contains(err.Error(), "request body too large")
}

func respondError(c *gin.Context, status int, message string) {
	c.JSON(status, errorResponse{Error: message})
}
