package sheets

import (
	"testing"
	"time"

	"github.com/nasiraliev/voiceline/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildRow(t *testing.T) {
	ts := time.Date(2026, 6, 23, 10, 30, 0, 0, time.UTC)
	v := domain.Voiceline{
		JobID:          "job-123",
		CreatedAt:      ts,
		SourceFilename: "call.m4a",
		Analysis: domain.Analysis{
			Title:     "Acme renewal",
			Summary:   "Discussed Q3 renewal",
			KeyPoints: []string{"budget approved", "needs legal review"},
			ActionItems: []domain.ActionItem{
				{Task: "Send contract", Owner: "Sam", Due: "Friday"},
				{Task: "Schedule demo"},
			},
			Transcript: "full transcript here",
		},
	}

	row := buildRow(v)
	require.Len(t, row, len(headerRow))
	assert.Equal(t, "2026-06-23T10:30:00Z", row[0])
	assert.Equal(t, "job-123", row[1])
	assert.Equal(t, "call.m4a", row[2])
	assert.Equal(t, "Acme renewal", row[3])
	assert.Equal(t, "Discussed Q3 renewal", row[4])
	assert.Equal(t, "budget approved\nneeds legal review", row[5])
	assert.Equal(t, "Send contract - Sam (Friday)\nSchedule demo", row[6])
	assert.Equal(t, "full transcript here", row[7])
}

func TestBuildRowEmptyCollections(t *testing.T) {
	v := domain.Voiceline{
		JobID:     "j",
		CreatedAt: time.Unix(0, 0).UTC(),
		Analysis:  domain.Analysis{Title: "t"},
	}
	row := buildRow(v)
	assert.Equal(t, "", row[5])
	assert.Equal(t, "", row[6])
}

func TestFormatActionItems(t *testing.T) {
	assert.Equal(t, "", formatActionItems(nil))
	assert.Equal(t, "Task only", formatActionItems([]domain.ActionItem{{Task: "Task only"}}))
	assert.Equal(t, "Call back - Alice", formatActionItems([]domain.ActionItem{{Task: "Call back", Owner: "Alice"}}))
	assert.Equal(t, "Pay invoice (Mon)", formatActionItems([]domain.ActionItem{{Task: "Pay invoice", Due: "Mon"}}))
	assert.Equal(t, "Do it - Bob (Tue)", formatActionItems([]domain.ActionItem{{Task: "Do it", Owner: "Bob", Due: "Tue"}}))
}
