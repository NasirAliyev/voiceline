package logdest

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/nasiraliev/voiceline/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeliverLogsAndSucceeds(t *testing.T) {
	var buf bytes.Buffer
	d := New(slog.New(slog.NewJSONHandler(&buf, nil)))

	v := domain.Voiceline{
		JobID:          "job-1",
		SourceFilename: "x.m4a",
		Analysis: domain.Analysis{
			Title:      "Title",
			Summary:    "Summary",
			KeyPoints:  []string{"a", "b"},
			Transcript: "SECRET_TRANSCRIPT_TEXT",
		},
	}
	require.NoError(t, d.Deliver(context.Background(), v))

	out := buf.String()
	assert.Contains(t, out, "job-1")
	assert.Contains(t, out, "Title")
	// Logging hygiene: the full transcript must never be logged.
	assert.NotContains(t, out, "SECRET_TRANSCRIPT_TEXT")

	// Output is valid structured JSON.
	var entry map[string]any
	require.NoError(t, json.NewDecoder(strings.NewReader(out)).Decode(&entry))
}
