package domain

import (
	"fmt"
	"strings"
	"time"
)

// Transcript is the verbatim text produced from a recording.
type Transcript struct {
	Text     string
	Language string // optional BCP-47 hint/result; empty if unknown
}

// NewTranscript constructs a Transcript, requiring non-empty text.
func NewTranscript(text, language string) (Transcript, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Transcript{}, fmt.Errorf("%w: transcript text is required", ErrInvalidArgument)
	}
	return Transcript{Text: text, Language: strings.TrimSpace(language)}, nil
}

// ActionItem is a single follow-up extracted from a voice note. Owner and Due
// are optional and empty when the speaker did not state them.
type ActionItem struct {
	Task  string
	Owner string
	Due   string
}

// NewActionItem constructs an ActionItem, requiring a non-empty task.
func NewActionItem(task, owner, due string) (ActionItem, error) {
	task = strings.TrimSpace(task)
	if task == "" {
		return ActionItem{}, fmt.Errorf("%w: action item task is required", ErrInvalidArgument)
	}
	return ActionItem{Task: task, Owner: strings.TrimSpace(owner), Due: strings.TrimSpace(due)}, nil
}

// Analysis is the structured note extracted from a transcript — the canonical,
// provider-agnostic shape the whole system speaks. Adapters map their provider
// output into this type; nothing downstream depends on the LLM's wire format.
type Analysis struct {
	Title       string
	Summary     string
	KeyPoints   []string
	ActionItems []ActionItem
	Transcript  string
}

// AudioMeta carries non-sensitive metadata about an uploaded recording. The
// audio bytes themselves are never held on the domain model.
type AudioMeta struct {
	Filename    string
	ContentType string
	Size        int64
}

// Voiceline is the delivered aggregate: the published view of a processed voice
// note that a Destination receives. It is deliberately separate from Job (the
// processing unit) so delivery never depends on internal job bookkeeping.
type Voiceline struct {
	JobID          string
	CreatedAt      time.Time
	SourceFilename string
	Analysis       Analysis
}
