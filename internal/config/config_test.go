package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadDefaults(t *testing.T) {
	// Only the required secret is set; everything else should fall to defaults.
	t.Setenv("OPENAI_API_KEY", "sk-test")

	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, defaultPort, cfg.Server.Port)
	assert.Equal(t, defaultServerReadTimeout, cfg.Server.ReadTimeout)
	assert.Equal(t, defaultShutdownTimeout, cfg.Server.ShutdownTimeout)
	assert.Equal(t, defaultOpenAIBaseURL, cfg.OpenAI.BaseURL)
	assert.Equal(t, defaultTranscriptionModel, cfg.OpenAI.TranscriptionModel)
	assert.Equal(t, defaultAnalysisModel, cfg.OpenAI.AnalysisModel)
	assert.Equal(t, defaultOpenAIMaxRetries, cfg.OpenAI.MaxRetries)
	assert.Equal(t, DestinationLog, cfg.Sink.Destination)
	assert.Equal(t, defaultWorkers, cfg.Pipeline.Workers)
	assert.Equal(t, defaultQueueSize, cfg.Pipeline.QueueSize)
	assert.Equal(t, int64(defaultMaxAudioBytes), cfg.Upload.MaxAudioBytes)
	assert.Equal(t, defaultAllowedAudioTypes, cfg.Upload.AllowedAudioTypes)
	assert.False(t, cfg.AuthEnabled())
	assert.Equal(t, LogFormatJSON, cfg.Log.Format)
}

func TestLoadOverrides(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-test")
	t.Setenv("PORT", "9090")
	t.Setenv("SERVER_READ_TIMEOUT", "5s")
	t.Setenv("OPENAI_BASE_URL", "https://proxy.example.com/v1/") // trailing slash trimmed
	t.Setenv("OPENAI_ANALYSIS_MODEL", "gpt-4o")
	t.Setenv("PIPELINE_WORKERS", "8")
	t.Setenv("MAX_AUDIO_BYTES", "1048576")
	t.Setenv("ALLOWED_AUDIO_TYPES", "audio/mpeg, audio/wav ,") // spaces + trailing empty
	t.Setenv("API_KEY", "secret")
	t.Setenv("LOG_FORMAT", "TEXT") // case-insensitive

	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, "9090", cfg.Server.Port)
	assert.Equal(t, 5*time.Second, cfg.Server.ReadTimeout)
	assert.Equal(t, "https://proxy.example.com/v1", cfg.OpenAI.BaseURL)
	assert.Equal(t, "gpt-4o", cfg.OpenAI.AnalysisModel)
	assert.Equal(t, 8, cfg.Pipeline.Workers)
	assert.Equal(t, int64(1048576), cfg.Upload.MaxAudioBytes)
	assert.Equal(t, []string{"audio/mpeg", "audio/wav"}, cfg.Upload.AllowedAudioTypes)
	assert.True(t, cfg.AuthEnabled())
	assert.Equal(t, LogFormatText, cfg.Log.Format)
}

func TestLoadValidationErrors(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{"missing api key", map[string]string{}},
		{"unknown destination", map[string]string{"OPENAI_API_KEY": "k", "DESTINATION": "carrier-pigeon"}},
		{"sheets without spreadsheet id", map[string]string{"OPENAI_API_KEY": "k", "DESTINATION": "sheets"}},
		{"workers below one", map[string]string{"OPENAI_API_KEY": "k", "PIPELINE_WORKERS": "0"}},
		{"malformed duration", map[string]string{"OPENAI_API_KEY": "k", "SERVER_READ_TIMEOUT": "soon"}},
		{"malformed integer", map[string]string{"OPENAI_API_KEY": "k", "MAX_AUDIO_BYTES": "lots"}},
		{"unknown log format", map[string]string{"OPENAI_API_KEY": "k", "LOG_FORMAT": "xml"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			_, err := Load()
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrInvalidConfig)
		})
	}
}

func TestLoadSheetsDestinationValid(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-test")
	t.Setenv("DESTINATION", "sheets")
	t.Setenv("GOOGLE_SHEETS_SPREADSHEET_ID", "sheet-123")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, DestinationSheets, cfg.Sink.Destination)
	assert.Equal(t, "sheet-123", cfg.Sink.SpreadsheetID)
	assert.Equal(t, defaultSheetName, cfg.Sink.SheetName)
}
