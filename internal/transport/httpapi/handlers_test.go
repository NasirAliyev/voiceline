package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nasiraliev/voiceline/internal/adapter/memstore"
	"github.com/nasiraliev/voiceline/internal/app"
	"github.com/nasiraliev/voiceline/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

var testAllowedTypes = []string{"audio/mpeg", "audio/mp4", "audio/m4a"}

// fakePool captures submitted tasks and can simulate backpressure.
type fakePool struct {
	mu    sync.Mutex
	tasks []app.Task
	err   error
}

func (f *fakePool) Submit(task app.Task) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.tasks = append(f.tasks, task)
	return nil
}

func (f *fakePool) submitted() []app.Task {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tasks
}

func setup(t *testing.T, pool submitter, authToken string, maxBytes int64) (*gin.Engine, *memstore.Store) {
	t.Helper()
	store := memstore.New()
	h := NewHandlers(store, pool, testLogger(), maxBytes, testAllowedTypes, t.TempDir())
	return NewRouter(h, testLogger(), authToken), store
}

func multipartBody(t *testing.T, field, filename, contentType string, data []byte) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	hdr := make(textproto.MIMEHeader)
	hdr.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, field, filename))
	if contentType != "" {
		hdr.Set("Content-Type", contentType)
	}
	part, err := w.CreatePart(hdr)
	require.NoError(t, err)
	_, err = part.Write(data)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return body, w.FormDataContentType()
}

func postAudio(t *testing.T, r *gin.Engine, body *bytes.Buffer, contentType string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, RouteSubmit, body)
	req.Header.Set("Content-Type", contentType)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestHealth(t *testing.T) {
	// Health stays open even when auth is configured.
	r, _ := setup(t, &fakePool{}, "a-token", 1<<20)
	req := httptest.NewRequest(http.MethodGet, RouteHealth, nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestSubmitHappyPath(t *testing.T) {
	pool := &fakePool{}
	r, store := setup(t, pool, "", 1<<20)

	body, ct := multipartBody(t, formFieldAudio, "note.m4a", "audio/m4a", []byte("fake-audio-bytes"))
	rec := postAudio(t, r, body, ct, nil)

	require.Equal(t, http.StatusAccepted, rec.Code)
	var resp submitResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.NotEmpty(t, resp.ID)
	assert.Equal(t, string(domain.StatusQueued), resp.Status)
	assert.Contains(t, resp.StatusURL, resp.ID)
	assert.NotEmpty(t, rec.Header().Get(headerRequestID))

	// Job persisted as queued.
	job, err := store.Get(context.Background(), resp.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusQueued, job.Status)

	// Task enqueued; its opener yields the spooled audio.
	tasks := pool.submitted()
	require.Len(t, tasks, 1)
	assert.Equal(t, resp.ID, tasks[0].JobID)
	rc, err := tasks[0].Open()
	require.NoError(t, err)
	data, _ := io.ReadAll(rc)
	_ = rc.Close()
	assert.Equal(t, "fake-audio-bytes", string(data))
}

func TestSubmitValidation(t *testing.T) {
	tests := []struct {
		name        string
		filename    string
		contentType string
		data        []byte
		maxBytes    int64
		wantStatus  int
	}{
		{"empty file", "a.m4a", "audio/m4a", []byte{}, 1 << 20, http.StatusBadRequest},
		{"unsupported type", "a.txt", "text/plain", []byte("hello there"), 1 << 20, http.StatusUnsupportedMediaType},
		{"missing content type", "a.m4a", "", []byte("hello there"), 1 << 20, http.StatusUnsupportedMediaType},
		{"oversized", "a.m4a", "audio/m4a", bytes.Repeat([]byte("x"), 500), 100, http.StatusRequestEntityTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := &fakePool{}
			r, _ := setup(t, pool, "", tt.maxBytes)
			body, ct := multipartBody(t, formFieldAudio, tt.filename, tt.contentType, tt.data)
			rec := postAudio(t, r, body, ct, nil)
			assert.Equal(t, tt.wantStatus, rec.Code)
			assert.Empty(t, pool.submitted(), "no task should be enqueued on validation failure")
		})
	}
}

func TestSubmitMissingFileField(t *testing.T) {
	pool := &fakePool{}
	r, _ := setup(t, pool, "", 1<<20)
	body, ct := multipartBody(t, "not_audio", "a.m4a", "audio/m4a", []byte("data"))
	rec := postAudio(t, r, body, ct, nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Empty(t, pool.submitted())
}

func TestSubmitQueueFull(t *testing.T) {
	pool := &fakePool{err: domain.ErrQueueFull}
	r, _ := setup(t, pool, "", 1<<20)
	body, ct := multipartBody(t, formFieldAudio, "a.m4a", "audio/m4a", []byte("data"))
	rec := postAudio(t, r, body, ct, nil)

	// Backpressure surfaces as 503 with a Retry-After hint.
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Equal(t, retryAfterValue, rec.Header().Get(headerRetryAfter))
}

func TestAuth(t *testing.T) {
	const token = "secret-token"
	tests := []struct {
		name       string
		headers    map[string]string
		wantStatus int
	}{
		{"no token", nil, http.StatusUnauthorized},
		{"wrong bearer", map[string]string{"Authorization": "Bearer nope"}, http.StatusUnauthorized},
		{"valid bearer", map[string]string{"Authorization": "Bearer " + token}, http.StatusAccepted},
		{"valid api key", map[string]string{"X-API-Key": token}, http.StatusAccepted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, _ := setup(t, &fakePool{}, token, 1<<20)
			body, ct := multipartBody(t, formFieldAudio, "a.m4a", "audio/m4a", []byte("data"))
			rec := postAudio(t, r, body, ct, tt.headers)
			assert.Equal(t, tt.wantStatus, rec.Code)
		})
	}
}

func TestStatusUnknownReturns404(t *testing.T) {
	r, _ := setup(t, &fakePool{}, "", 1<<20)
	req := httptest.NewRequest(http.MethodGet, statusURLPrefix+"does-not-exist", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestStatusReturnsStates(t *testing.T) {
	r, store := setup(t, &fakePool{}, "", 1<<20)

	completed, _ := domain.NewJob("job-done", domain.AudioMeta{Filename: "a.m4a"}, time.Now())
	completed.Complete(domain.Analysis{
		Title:       "Title",
		Summary:     "Sum",
		KeyPoints:   []string{"k1"},
		ActionItems: []domain.ActionItem{{Task: "do", Owner: "me"}},
		Transcript:  "transcript",
	}, time.Now())
	require.NoError(t, store.Create(context.Background(), completed))

	failed, _ := domain.NewJob("job-failed", domain.AudioMeta{}, time.Now())
	failed.Fail("transcription failed", time.Now())
	require.NoError(t, store.Create(context.Background(), failed))

	t.Run("completed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, statusURLPrefix+"job-done", nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)

		var resp statusResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, string(domain.StatusCompleted), resp.Status)
		require.NotNil(t, resp.Result)
		assert.Equal(t, "Title", resp.Result.Title)
		require.Len(t, resp.Result.ActionItems, 1)
		assert.Equal(t, "do", resp.Result.ActionItems[0].Task)
		assert.Empty(t, resp.Error)
	})

	t.Run("failed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, statusURLPrefix+"job-failed", nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)

		var resp statusResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, string(domain.StatusFailed), resp.Status)
		assert.Equal(t, "transcription failed", resp.Error)
		assert.Nil(t, resp.Result)
	})
}
