package sheets

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nasiraliev/voiceline/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeAppender struct {
	gotSpreadsheetID string
	gotRange         string
	gotValues        []any
	err              error
	calls            int
}

func (f *fakeAppender) Append(_ context.Context, spreadsheetID, writeRange string, values []any) error {
	f.calls++
	f.gotSpreadsheetID = spreadsheetID
	f.gotRange = writeRange
	f.gotValues = values
	return f.err
}

func TestDestinationDeliver(t *testing.T) {
	fake := &fakeAppender{}
	d := &Destination{appender: fake, spreadsheetID: "sheet-1", sheetName: "Voicelines"}

	v := domain.Voiceline{
		JobID:     "j1",
		CreatedAt: time.Unix(0, 0).UTC(),
		Analysis:  domain.Analysis{Title: "T", Summary: "S"},
	}
	require.NoError(t, d.Deliver(context.Background(), v))

	assert.Equal(t, 1, fake.calls)
	assert.Equal(t, "sheet-1", fake.gotSpreadsheetID)
	assert.Equal(t, "Voicelines!A1", fake.gotRange)
	require.Len(t, fake.gotValues, len(headerRow))
	assert.Equal(t, "j1", fake.gotValues[1])
	assert.Equal(t, "T", fake.gotValues[3])
}

func TestDestinationDeliverError(t *testing.T) {
	fake := &fakeAppender{err: errors.New("403 forbidden")}
	d := &Destination{appender: fake, spreadsheetID: "s", sheetName: "Sheet1"}

	err := d.Deliver(context.Background(), domain.Voiceline{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "append row")
	assert.Contains(t, err.Error(), "403")
}

// TestDestinationDeliverNotRetried documents that a single Deliver performs
// exactly one append (no internal retry of the non-idempotent mutation).
func TestDestinationDeliverNotRetried(t *testing.T) {
	fake := &fakeAppender{err: errors.New("boom")}
	d := &Destination{appender: fake, spreadsheetID: "s", sheetName: "Sheet1"}

	_ = d.Deliver(context.Background(), domain.Voiceline{})
	assert.Equal(t, 1, fake.calls)
}
