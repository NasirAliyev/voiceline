// Package httpapi is the HTTP transport: Gin router, handlers, middleware, and
// request/response DTOs. It depends on the app and domain layers but never the
// reverse. (Named httpapi, not http, to avoid colliding with the net/http
// identifier at call sites.)
package httpapi

import (
	"time"

	"github.com/nasiraliev/voiceline/internal/domain"
)

// submitResponse is returned by POST /api/v1/voicelines (202 Accepted).
type submitResponse struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	StatusURL string `json:"status_url"`
}

// statusResponse is returned by GET /api/v1/voicelines/:id.
type statusResponse struct {
	ID        string       `json:"id"`
	Status    string       `json:"status"`
	CreatedAt time.Time    `json:"created_at"`
	UpdatedAt time.Time    `json:"updated_at"`
	Result    *analysisDTO `json:"result,omitempty"`
	Error     string       `json:"error,omitempty"`
}

type analysisDTO struct {
	Title       string          `json:"title"`
	Summary     string          `json:"summary"`
	KeyPoints   []string        `json:"key_points"`
	ActionItems []actionItemDTO `json:"action_items"`
	Transcript  string          `json:"transcript"`
}

type actionItemDTO struct {
	Task  string `json:"task"`
	Owner string `json:"owner,omitempty"`
	Due   string `json:"due,omitempty"`
}

// errorResponse is the uniform error envelope.
type errorResponse struct {
	Error string `json:"error"`
}

// newStatusResponse maps a domain Job into its API representation, keeping the
// transport free of domain types on the wire.
func newStatusResponse(job *domain.Job) statusResponse {
	resp := statusResponse{
		ID:        job.ID,
		Status:    string(job.Status),
		CreatedAt: job.CreatedAt,
		UpdatedAt: job.UpdatedAt,
		Error:     job.Err,
	}
	if job.Result != nil {
		resp.Result = newAnalysisDTO(*job.Result)
	}
	return resp
}

func newAnalysisDTO(a domain.Analysis) *analysisDTO {
	keyPoints := a.KeyPoints
	if keyPoints == nil {
		keyPoints = []string{}
	}
	items := make([]actionItemDTO, 0, len(a.ActionItems))
	for _, it := range a.ActionItems {
		items = append(items, actionItemDTO{Task: it.Task, Owner: it.Owner, Due: it.Due})
	}
	return &analysisDTO{
		Title:       a.Title,
		Summary:     a.Summary,
		KeyPoints:   keyPoints,
		ActionItems: items,
		Transcript:  a.Transcript,
	}
}
