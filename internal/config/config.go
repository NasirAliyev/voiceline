// Package config loads and validates all runtime configuration from the
// environment exactly once at startup. Every tunable has a const default; only
// secrets are mandatory. Loading never calls os.Exit/log.Fatal — the caller
// decides how to handle an invalid configuration.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// ErrInvalidConfig is the sentinel wrapped by all configuration failures.
var ErrInvalidConfig = errors.New("invalid configuration")

// Destination kinds (value of the DESTINATION variable).
const (
	DestinationLog    = "log"
	DestinationSheets = "sheets"
)

// Log output formats (value of LOG_FORMAT).
const (
	LogFormatJSON = "json"
	LogFormatText = "text"
)

// Default values for every tunable. Secrets intentionally have no default.
const (
	defaultPort                 = "8080"
	defaultServerReadTimeout    = 15 * time.Second
	defaultServerWriteTimeout   = 15 * time.Second
	defaultShutdownTimeout      = 30 * time.Second
	defaultOpenAIBaseURL        = "https://api.openai.com/v1"
	defaultTranscriptionModel   = "whisper-1"
	defaultAnalysisModel        = "gpt-4o-mini"
	defaultOpenAIRequestTimeout = 60 * time.Second
	defaultOpenAIMaxRetries     = 2
	defaultSheetName            = "Voicelines"
	defaultWorkers              = 4
	defaultQueueSize            = 32
	defaultProcessingTimeout    = 120 * time.Second
	defaultMaxAudioBytes        = 26214400 // 25 MiB — the Whisper upload limit
	defaultLogLevel             = "info"
)

// defaultAllowedAudioTypes is the MIME allowlist applied when ALLOWED_AUDIO_TYPES
// is unset. Covers the formats the OpenAI transcription endpoint accepts.
var defaultAllowedAudioTypes = []string{
	"audio/mpeg",  // .mp3
	"audio/mp4",   // .mp4 / .m4a
	"audio/m4a",   // .m4a (some clients)
	"audio/x-m4a", // .m4a (Apple)
	"audio/wav",   // .wav
	"audio/x-wav", // .wav (some clients)
	"audio/webm",  // .webm
	"audio/ogg",   // .ogg / .oga
	"audio/flac",  // .flac
}

// Config holds all runtime configuration, grouped by concern.
type Config struct {
	Server   ServerConfig
	OpenAI   OpenAIConfig
	Sink     SinkConfig
	Pipeline PipelineConfig
	Upload   UploadConfig
	Auth     AuthConfig
	Log      LogConfig
}

// ServerConfig configures the inbound HTTP server.
type ServerConfig struct {
	Port            string
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	ShutdownTimeout time.Duration
}

// OpenAIConfig configures the OpenAI-compatible LLM adapter.
type OpenAIConfig struct {
	APIKey             string
	BaseURL            string
	TranscriptionModel string
	AnalysisModel      string
	RequestTimeout     time.Duration
	MaxRetries         int
}

// SinkConfig configures the delivery destination.
type SinkConfig struct {
	Destination     string
	SpreadsheetID   string
	SheetName       string
	CredentialsFile string
}

// PipelineConfig configures the asynchronous worker pool.
type PipelineConfig struct {
	Workers           int
	QueueSize         int
	ProcessingTimeout time.Duration
}

// UploadConfig configures inbound audio limits.
type UploadConfig struct {
	MaxAudioBytes     int64
	AllowedAudioTypes []string
}

// AuthConfig configures optional bearer-token auth. An empty APIKey disables it.
type AuthConfig struct {
	APIKey string
}

// LogConfig configures structured logging.
type LogConfig struct {
	Level  string
	Format string
}

// AuthEnabled reports whether bearer-token auth should be enforced.
func (c Config) AuthEnabled() bool { return c.Auth.APIKey != "" }

// Load reads configuration from the environment, loading a local .env first if
// present (dev convenience only — never required in production), applies the
// const defaults, and validates the result.
func Load() (Config, error) {
	// A missing .env is not an error: production injects real environment vars.
	_ = godotenv.Load()

	l := &loader{}
	cfg := Config{
		Server: ServerConfig{
			Port:            l.str("PORT", defaultPort),
			ReadTimeout:     l.dur("SERVER_READ_TIMEOUT", defaultServerReadTimeout),
			WriteTimeout:    l.dur("SERVER_WRITE_TIMEOUT", defaultServerWriteTimeout),
			ShutdownTimeout: l.dur("SERVER_SHUTDOWN_TIMEOUT", defaultShutdownTimeout),
		},
		OpenAI: OpenAIConfig{
			APIKey:             l.str("OPENAI_API_KEY", ""),
			BaseURL:            strings.TrimRight(l.str("OPENAI_BASE_URL", defaultOpenAIBaseURL), "/"),
			TranscriptionModel: l.str("OPENAI_TRANSCRIPTION_MODEL", defaultTranscriptionModel),
			AnalysisModel:      l.str("OPENAI_ANALYSIS_MODEL", defaultAnalysisModel),
			RequestTimeout:     l.dur("OPENAI_REQUEST_TIMEOUT", defaultOpenAIRequestTimeout),
			MaxRetries:         l.intVal("OPENAI_MAX_RETRIES", defaultOpenAIMaxRetries),
		},
		Sink: SinkConfig{
			Destination:     strings.ToLower(l.str("DESTINATION", DestinationLog)),
			SpreadsheetID:   l.str("GOOGLE_SHEETS_SPREADSHEET_ID", ""),
			SheetName:       l.str("GOOGLE_SHEETS_SHEET_NAME", defaultSheetName),
			CredentialsFile: l.str("GOOGLE_APPLICATION_CREDENTIALS", ""),
		},
		Pipeline: PipelineConfig{
			Workers:           l.intVal("PIPELINE_WORKERS", defaultWorkers),
			QueueSize:         l.intVal("PIPELINE_QUEUE_SIZE", defaultQueueSize),
			ProcessingTimeout: l.dur("PIPELINE_PROCESSING_TIMEOUT", defaultProcessingTimeout),
		},
		Upload: UploadConfig{
			MaxAudioBytes:     l.int64Val("MAX_AUDIO_BYTES", defaultMaxAudioBytes),
			AllowedAudioTypes: l.strSlice("ALLOWED_AUDIO_TYPES", defaultAllowedAudioTypes),
		},
		Auth: AuthConfig{
			APIKey: l.str("API_KEY", ""),
		},
		Log: LogConfig{
			Level:  strings.ToLower(l.str("LOG_LEVEL", defaultLogLevel)),
			Format: strings.ToLower(l.str("LOG_FORMAT", LogFormatJSON)),
		},
	}

	if len(l.errs) > 0 {
		return Config{}, fmt.Errorf("%w: %w", ErrInvalidConfig, errors.Join(l.errs...))
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// validate enforces cross-field and required-value invariants.
func (c Config) validate() error {
	if c.OpenAI.APIKey == "" {
		return fmt.Errorf("%w: OPENAI_API_KEY is required", ErrInvalidConfig)
	}
	if c.Pipeline.Workers < 1 {
		return fmt.Errorf("%w: PIPELINE_WORKERS must be >= 1", ErrInvalidConfig)
	}
	if c.Pipeline.QueueSize < 1 {
		return fmt.Errorf("%w: PIPELINE_QUEUE_SIZE must be >= 1", ErrInvalidConfig)
	}
	if c.Upload.MaxAudioBytes < 1 {
		return fmt.Errorf("%w: MAX_AUDIO_BYTES must be >= 1", ErrInvalidConfig)
	}
	switch c.Sink.Destination {
	case DestinationLog:
		// No additional requirements — the zero-setup default.
	case DestinationSheets:
		if c.Sink.SpreadsheetID == "" {
			return fmt.Errorf("%w: GOOGLE_SHEETS_SPREADSHEET_ID is required when DESTINATION=%s", ErrInvalidConfig, DestinationSheets)
		}
	default:
		return fmt.Errorf("%w: unknown DESTINATION %q (want %q or %q)", ErrInvalidConfig, c.Sink.Destination, DestinationLog, DestinationSheets)
	}
	switch c.Log.Format {
	case LogFormatJSON, LogFormatText:
	default:
		return fmt.Errorf("%w: unknown LOG_FORMAT %q (want %q or %q)", ErrInvalidConfig, c.Log.Format, LogFormatJSON, LogFormatText)
	}
	return nil
}

// loader reads typed values from the environment, accumulating parse errors so
// Load can report every malformed variable at once rather than failing fast.
type loader struct{ errs []error }

func (l *loader) str(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func (l *loader) intVal(key string, def int) int {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		l.errs = append(l.errs, fmt.Errorf("%s: invalid integer %q", key, v))
		return def
	}
	return n
}

func (l *loader) int64Val(key string, def int64) int64 {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		l.errs = append(l.errs, fmt.Errorf("%s: invalid integer %q", key, v))
		return def
	}
	return n
}

func (l *loader) dur(key string, def time.Duration) time.Duration {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		l.errs = append(l.errs, fmt.Errorf("%s: invalid duration %q", key, v))
		return def
	}
	return d
}

func (l *loader) strSlice(key string, def []string) []string {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return def
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return def
	}
	return out
}
