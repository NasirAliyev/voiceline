package openai

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nasiraliev/voiceline/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// newTestClient builds a client with backoff disabled so retry tests are fast.
func newTestClient(baseURL string, retries int) *Client {
	c := NewClient(baseURL, "test-key", 5*time.Second, retries, testLogger())
	c.backoffFor = func(int) time.Duration { return 0 }
	return c
}

func openerFrom(s string) domain.AudioOpener {
	return func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(s)), nil
	}
}

// --- transcriber -------------------------------------------------------------

func TestTranscriberHappyPath(t *testing.T) {
	var (
		gotModel   string
		gotFileLen int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Contains(t, r.URL.Path, transcriptionPath)
		assert.True(t, strings.HasPrefix(r.Header.Get(headerAuthorization), bearerPrefix))

		require.NoError(t, r.ParseMultipartForm(1<<20))
		gotModel = r.FormValue(fieldModel)
		f, _, err := r.FormFile(fieldFile)
		require.NoError(t, err)
		b, _ := io.ReadAll(f)
		_ = f.Close()
		gotFileLen = len(b)

		w.Header().Set(headerContentType, mimeJSON)
		_, _ = w.Write([]byte(`{"text":"  hello world  "}`))
	}))
	defer srv.Close()

	tr := NewTranscriber(newTestClient(srv.URL, 2), "whisper-1")
	transcript, err := tr.Transcribe(context.Background(), openerFrom("AUDIO"), domain.AudioMeta{Filename: "note.m4a"})
	require.NoError(t, err)

	assert.Equal(t, "hello world", transcript.Text) // trimmed on construction
	assert.Equal(t, "whisper-1", gotModel)
	assert.Equal(t, len("AUDIO"), gotFileLen)
}

// TestTranscriberReopensAudioOnRetry is the regression guard for the single-use
// reader bug: a retry must rebuild the multipart body from a fresh stream, so
// the second request carries the full audio rather than an empty body.
func TestTranscriberReopensAudioOnRetry(t *testing.T) {
	var (
		mu       sync.Mutex
		calls    int
		fileLens []int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseMultipartForm(1<<20))
		f, _, err := r.FormFile(fieldFile)
		require.NoError(t, err)
		b, _ := io.ReadAll(f)
		_ = f.Close()

		mu.Lock()
		calls++
		n := calls
		fileLens = append(fileLens, len(b))
		mu.Unlock()

		if n == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"transient"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"text":"recovered"}`))
	}))
	defer srv.Close()

	var opens int32
	open := func() (io.ReadCloser, error) {
		atomic.AddInt32(&opens, 1)
		return io.NopCloser(strings.NewReader("AUDIODATA")), nil
	}

	tr := NewTranscriber(newTestClient(srv.URL, 2), "whisper-1")
	transcript, err := tr.Transcribe(context.Background(), open, domain.AudioMeta{Filename: "a.m4a"})
	require.NoError(t, err)

	assert.Equal(t, "recovered", transcript.Text)
	assert.Equal(t, int32(2), atomic.LoadInt32(&opens), "audio must be re-opened for the retry")
	require.Len(t, fileLens, 2)
	assert.Equal(t, len("AUDIODATA"), fileLens[0])
	assert.Equal(t, len("AUDIODATA"), fileLens[1], "retry must resend the full audio body, not an empty one")
}

func TestTranscriberNonRetryableError(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad file"}}`))
	}))
	defer srv.Close()

	tr := NewTranscriber(newTestClient(srv.URL, 2), "whisper-1")
	_, err := tr.Transcribe(context.Background(), openerFrom("x"), domain.AudioMeta{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "400")
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls), "4xx must not be retried")
}

func TestTranscriberExhaustsRetries(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"down"}}`))
	}))
	defer srv.Close()

	tr := NewTranscriber(newTestClient(srv.URL, 2), "whisper-1")
	_, err := tr.Transcribe(context.Background(), openerFrom("x"), domain.AudioMeta{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "500")
	assert.Equal(t, int32(3), atomic.LoadInt32(&calls), "should try once plus maxRetries")
}

func TestTranscriberEmptyTextFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"text":"   "}`))
	}))
	defer srv.Close()

	tr := NewTranscriber(newTestClient(srv.URL, 0), "whisper-1")
	_, err := tr.Transcribe(context.Background(), openerFrom("x"), domain.AudioMeta{})
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrInvalidArgument)
}

// --- analyzer ----------------------------------------------------------------

func TestAnalyzerHappyPath(t *testing.T) {
	var reqSeen chatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.Path, chatCompletionsPath)
		assert.Equal(t, mimeJSON, r.Header.Get(headerContentType))
		require.NoError(t, json.NewDecoder(r.Body).Decode(&reqSeen))

		// key_points has a blank entry and action_items has a blank-task entry;
		// both must be dropped by the mapping.
		content := `{"title":"Acme sync","summary":"Renewal discussed",` +
			`"key_points":["renewal Q3","  "],` +
			`"action_items":[{"task":"Send quote","owner":"Sam","due":"Fri"},{"task":" ","owner":"","due":""}]}`
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": content}}},
		})
	}))
	defer srv.Close()

	an := NewAnalyzer(newTestClient(srv.URL, 2), "gpt-4o-mini")
	got, err := an.Analyze(context.Background(), domain.Transcript{Text: "we met acme"})
	require.NoError(t, err)

	assert.Equal(t, "Acme sync", got.Title)
	assert.Equal(t, "Renewal discussed", got.Summary)
	assert.Equal(t, []string{"renewal Q3"}, got.KeyPoints)
	require.Len(t, got.ActionItems, 1)
	assert.Equal(t, "Send quote", got.ActionItems[0].Task)
	assert.Equal(t, "Sam", got.ActionItems[0].Owner)

	// Request used Structured Outputs against the configured model.
	assert.Equal(t, "gpt-4o-mini", reqSeen.Model)
	assert.Equal(t, "json_schema", reqSeen.ResponseFormat.Type)
	assert.True(t, reqSeen.ResponseFormat.JSONSchema.Strict)
	require.Len(t, reqSeen.Messages, 2)
	assert.Equal(t, roleSystem, reqSeen.Messages[0].Role)
	assert.Contains(t, reqSeen.Messages[1].Content, "we met acme")
}

func TestAnalyzerRetriesOn429(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
			return
		}
		content := `{"title":"T","summary":"S","key_points":[],"action_items":[]}`
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": content}}},
		})
	}))
	defer srv.Close()

	an := NewAnalyzer(newTestClient(srv.URL, 2), "gpt-4o-mini")
	got, err := an.Analyze(context.Background(), domain.Transcript{Text: "hi"})
	require.NoError(t, err)
	assert.Equal(t, "T", got.Title)
	assert.Equal(t, int32(2), atomic.LoadInt32(&calls))
}

func TestAnalyzerNonRetryableError(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid key"}}`))
	}))
	defer srv.Close()

	an := NewAnalyzer(newTestClient(srv.URL, 2), "gpt-4o-mini")
	_, err := an.Analyze(context.Background(), domain.Transcript{Text: "hi"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "401")
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls))
}

// --- helpers under test ------------------------------------------------------

func TestParseAPIError(t *testing.T) {
	withEnvelope := parseAPIError("analysis", http.StatusInternalServerError, []byte(`{"error":{"message":"boom"}}`))
	assert.Equal(t, http.StatusInternalServerError, withEnvelope.status)
	assert.Contains(t, withEnvelope.Error(), "boom")
	assert.Contains(t, withEnvelope.Error(), "http 500")

	rawBody := parseAPIError("transcription", http.StatusServiceUnavailable, []byte("service unavailable"))
	assert.Contains(t, rawBody.Error(), "service unavailable")
}

func TestIsRetryable(t *testing.T) {
	assert.True(t, isRetryable(http.StatusTooManyRequests))
	assert.True(t, isRetryable(http.StatusBadGateway))
	assert.False(t, isRetryable(http.StatusBadRequest))
	assert.False(t, isRetryable(http.StatusOK))
}
