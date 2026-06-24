package httpapi

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const (
	headerRequestID     = "X-Request-ID"
	headerAuthorization = "Authorization"
	headerAPIKey        = "X-API-Key"
	headerRetryAfter    = "Retry-After"
	bearerPrefix        = "Bearer "
	ctxKeyRequestID     = "request_id"
)

// requestID assigns or propagates a request id and echoes it back in the
// response so a client can correlate a job with server logs.
func requestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(headerRequestID)
		if id == "" {
			id = uuid.NewString()
		}
		c.Set(ctxKeyRequestID, id)
		c.Header(headerRequestID, id)
		c.Next()
	}
}

// recovery turns a panic into a 500 (instead of crashing the process), logging
// the cause with the request id.
func recovery(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				logger.Error("recovered from panic",
					slog.Any("panic", r),
					slog.String("request_id", requestIDFrom(c)),
					slog.String("path", c.Request.URL.Path),
				)
				c.AbortWithStatusJSON(http.StatusInternalServerError, errorResponse{Error: "internal server error"})
			}
		}()
		c.Next()
	}
}

// accessLog emits one structured line per request once it completes.
func accessLog(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		logger.LogAttrs(c.Request.Context(), slog.LevelInfo, "http request",
			slog.String("request_id", requestIDFrom(c)),
			slog.String("method", c.Request.Method),
			slog.String("path", c.Request.URL.Path),
			slog.Int("status", c.Writer.Status()),
			slog.Duration("elapsed", time.Since(start)),
		)
	}
}

// auth enforces a bearer token (or X-API-Key) when one is configured. An empty
// expectedToken disables auth entirely.
func auth(expectedToken string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if expectedToken == "" {
			c.Next()
			return
		}
		if !tokenMatches(presentedToken(c), expectedToken) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, errorResponse{Error: "unauthorized"})
			return
		}
		c.Next()
	}
}

// presentedToken extracts the token from either the Authorization bearer header
// or the X-API-Key header.
func presentedToken(c *gin.Context) string {
	if h := c.GetHeader(headerAuthorization); strings.HasPrefix(h, bearerPrefix) {
		return strings.TrimPrefix(h, bearerPrefix)
	}
	return c.GetHeader(headerAPIKey)
}

// tokenMatches compares in constant time to avoid leaking the token via timing.
func tokenMatches(presented, expected string) bool {
	return subtle.ConstantTimeCompare([]byte(presented), []byte(expected)) == 1
}

func requestIDFrom(c *gin.Context) string {
	if v, ok := c.Get(ctxKeyRequestID); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
