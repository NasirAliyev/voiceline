package sheets

import (
	"strings"
	"time"

	"github.com/nasiraliev/voiceline/internal/domain"
)

const (
	timestampLayout = time.RFC3339
	listSeparator   = "\n"
	ownerSeparator  = " - "
)

// headerRow labels the appended columns. Exposed so a future setup step could
// seed an empty sheet; kept here as the single source of column order/count.
var headerRow = []any{
	"Timestamp", "Job ID", "Source File", "Title", "Summary",
	"Key Points", "Action Items", "Transcript",
}

// buildRow maps a Voiceline into one spreadsheet row. It is pure and
// unit-tested; the Sheets adapter only appends what this returns.
func buildRow(v domain.Voiceline) []any {
	return []any{
		v.CreatedAt.UTC().Format(timestampLayout),
		v.JobID,
		v.SourceFilename,
		v.Analysis.Title,
		v.Analysis.Summary,
		strings.Join(v.Analysis.KeyPoints, listSeparator),
		formatActionItems(v.Analysis.ActionItems),
		v.Analysis.Transcript,
	}
}

// formatActionItems renders items one per line as "task - owner (due)", omitting
// owner/due when absent.
func formatActionItems(items []domain.ActionItem) string {
	if len(items) == 0 {
		return ""
	}
	lines := make([]string, 0, len(items))
	for _, it := range items {
		line := it.Task
		if it.Owner != "" {
			line += ownerSeparator + it.Owner
		}
		if it.Due != "" {
			line += " (" + it.Due + ")"
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, listSeparator)
}
