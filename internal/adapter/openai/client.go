// Package openai is the reference LLM adapter: a thin client over the OpenAI
// HTTP API implementing domain.Transcriber and domain.Analyzer. It deliberately
// avoids the official SDK — fewer dependencies, and every request/response is
// trivially testable with httptest. Any OpenAI-compatible endpoint works via the
// configurable base URL, so swapping providers stays a one-adapter change.
package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// HTTP header names and the bearer prefix, kept as consts.
const (
	headerAuthorization = "Authorization"
	headerContentType   = "Content-Type"
	bearerPrefix        = "Bearer "
	mimeJSON            = "application/json"
)

const (
	baseBackoff      = 200 * time.Millisecond
	maxBackoff       = 5 * time.Second
	maxResponseBytes = 10 << 20 // 10 MiB cap on a response body (defensive)
	maxErrMessageLen = 300      // truncate provider error text before surfacing/logging
)

// Client is a minimal OpenAI-compatible HTTP client. It owns one shared
// *http.Client (connection pooling) and a bounded retry policy for transient
// failures. Safe for concurrent use.
type Client struct {
	baseURL    string
	apiKey     string
	maxRetries int
	httpClient *http.Client
	logger     *slog.Logger

	// backoffFor returns the delay before a given retry attempt. A field so
	// tests can disable sleeping; production uses the exponential default.
	backoffFor func(attempt int) time.Duration
}

// NewClient builds a client. requestTimeout bounds each individual attempt; the
// caller's context bounds the whole operation across retries.
func NewClient(baseURL, apiKey string, requestTimeout time.Duration, maxRetries int, logger *slog.Logger) *Client {
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     apiKey,
		maxRetries: maxRetries,
		httpClient: &http.Client{Timeout: requestTimeout},
		logger:     logger,
		backoffFor: defaultBackoff,
	}
}

// requestBuilder constructs a fresh *http.Request for one attempt. It MUST
// produce a new body each call so retries never send a drained reader — this is
// what makes the transcription multipart upload safe to retry.
type requestBuilder func(ctx context.Context) (*http.Request, error)

// do executes a request with bounded retries on transient failures (network
// errors, HTTP 429, and 5xx). It returns the response body for a 2xx response,
// or a descriptive error. The Authorization header is injected here so request
// builders never see the API key.
func (c *Client) do(ctx context.Context, op string, build requestBuilder) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if attempt > 0 {
			if err := wait(ctx, c.backoffFor(attempt)); err != nil {
				return nil, err
			}
			c.logger.DebugContext(ctx, "retrying openai request", slog.String("op", op), slog.Int("attempt", attempt))
		}

		req, err := build(ctx)
		if err != nil {
			return nil, fmt.Errorf("openai %s: build request: %w", op, err)
		}
		req.Header.Set(headerAuthorization, bearerPrefix+c.apiKey)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("openai %s: request failed: %w", op, err)
			continue // network errors are transient
		}

		body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
		_ = resp.Body.Close()
		if readErr != nil {
			lastErr = fmt.Errorf("openai %s: read response: %w", op, readErr)
			continue
		}

		if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
			return body, nil
		}

		apiErr := parseAPIError(op, resp.StatusCode, body)
		if isRetryable(resp.StatusCode) && attempt < c.maxRetries {
			lastErr = apiErr
			continue
		}
		return nil, apiErr
	}
	return nil, lastErr
}

// apiError describes a non-2xx OpenAI response.
type apiError struct {
	op      string
	status  int
	message string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("openai %s: http %d: %s", e.op, e.status, e.message)
}

// parseAPIError extracts the message from an OpenAI error envelope when present,
// falling back to the raw (truncated) body.
func parseAPIError(op string, status int, body []byte) *apiError {
	message := strings.TrimSpace(string(body))
	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &envelope) == nil && envelope.Error.Message != "" {
		message = envelope.Error.Message
	}
	if len(message) > maxErrMessageLen {
		message = message[:maxErrMessageLen]
	}
	return &apiError{op: op, status: status, message: message}
}

func isRetryable(status int) bool {
	return status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
}

func defaultBackoff(attempt int) time.Duration {
	d := baseBackoff << (attempt - 1) // 200ms, 400ms, 800ms, ...
	if d > maxBackoff {
		d = maxBackoff
	}
	return d
}

// wait sleeps for d or returns early if the context is cancelled.
func wait(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
