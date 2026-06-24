package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/nasiraliev/voiceline/internal/domain"
)

const (
	chatCompletionsPath = "/chat/completions"
	opAnalysis          = "analysis"
	analysisSchemaName  = "sales_note"
	maxOutputTokens     = 800

	roleSystem = "system"
	roleUser   = "user"

	systemPrompt = "You are a meticulous sales assistant. Extract a concise, structured " +
		"note from the meeting transcript. Use only information present in the transcript; " +
		"never invent details. Leave a field as an empty string or an empty list when the " +
		"transcript does not provide it."
	userPromptPrefix = "Extract the structured sales note from this transcript:\n\n"
)

// Analyzer implements domain.Analyzer using OpenAI chat completions with
// Structured Outputs, so the model is constrained to return schema-valid JSON.
type Analyzer struct {
	client *Client
	model  string
}

// NewAnalyzer builds an analyzer using the given client and model.
func NewAnalyzer(client *Client, model string) *Analyzer {
	return &Analyzer{client: client, model: model}
}

// --- request/response wire types --------------------------------------------

type chatRequest struct {
	Model          string         `json:"model"`
	Messages       []chatMessage  `json:"messages"`
	ResponseFormat responseFormat `json:"response_format"`
	MaxTokens      int            `json:"max_tokens"`
	Temperature    float64        `json:"temperature"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type responseFormat struct {
	Type       string     `json:"type"`
	JSONSchema jsonSchema `json:"json_schema"`
}

type jsonSchema struct {
	Name   string         `json:"name"`
	Strict bool           `json:"strict"`
	Schema map[string]any `json:"schema"`
}

// analysisPayload is the structured content the model returns (a JSON string in
// the completion), mirroring the schema below.
type analysisPayload struct {
	Title       string   `json:"title"`
	Summary     string   `json:"summary"`
	KeyPoints   []string `json:"key_points"`
	ActionItems []struct {
		Task  string `json:"task"`
		Owner string `json:"owner"`
		Due   string `json:"due"`
	} `json:"action_items"`
}

// Analyze extracts a structured Analysis from the transcript.
func (a *Analyzer) Analyze(ctx context.Context, transcript domain.Transcript) (domain.Analysis, error) {
	reqBody := chatRequest{
		Model: a.model,
		Messages: []chatMessage{
			{Role: roleSystem, Content: systemPrompt},
			{Role: roleUser, Content: userPromptPrefix + transcript.Text},
		},
		ResponseFormat: responseFormat{
			Type:       "json_schema",
			JSONSchema: jsonSchema{Name: analysisSchemaName, Strict: true, Schema: analysisSchema()},
		},
		MaxTokens:   maxOutputTokens,
		Temperature: 0, // deterministic extraction
	}
	raw, err := json.Marshal(reqBody)
	if err != nil {
		return domain.Analysis{}, fmt.Errorf("marshal analysis request: %w", err)
	}

	build := func(ctx context.Context) (*http.Request, error) {
		// A fresh bytes.Reader per attempt keeps retries safe (raw is immutable).
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.client.baseURL+chatCompletionsPath, bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		req.Header.Set(headerContentType, mimeJSON)
		return req, nil
	}

	respBody, err := a.client.do(ctx, opAnalysis, build)
	if err != nil {
		return domain.Analysis{}, err
	}

	var completion struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &completion); err != nil {
		return domain.Analysis{}, fmt.Errorf("decode completion: %w", err)
	}
	if len(completion.Choices) == 0 {
		return domain.Analysis{}, fmt.Errorf("analysis returned no choices")
	}

	var payload analysisPayload
	if err := json.Unmarshal([]byte(completion.Choices[0].Message.Content), &payload); err != nil {
		return domain.Analysis{}, fmt.Errorf("decode structured content: %w", err)
	}
	return payload.toDomain(), nil
}

// toDomain maps the provider payload into the canonical Analysis, trimming and
// dropping empty entries the model may emit to satisfy the strict schema.
func (p analysisPayload) toDomain() domain.Analysis {
	keyPoints := make([]string, 0, len(p.KeyPoints))
	for _, kp := range p.KeyPoints {
		if s := strings.TrimSpace(kp); s != "" {
			keyPoints = append(keyPoints, s)
		}
	}
	items := make([]domain.ActionItem, 0, len(p.ActionItems))
	for _, it := range p.ActionItems {
		task := strings.TrimSpace(it.Task)
		if task == "" {
			continue
		}
		items = append(items, domain.ActionItem{
			Task:  task,
			Owner: strings.TrimSpace(it.Owner),
			Due:   strings.TrimSpace(it.Due),
		})
	}
	return domain.Analysis{
		Title:       strings.TrimSpace(p.Title),
		Summary:     strings.TrimSpace(p.Summary),
		KeyPoints:   keyPoints,
		ActionItems: items,
	}
}

// analysisSchema is the strict JSON schema constraining the model's output.
// Strict mode requires every property to be listed in "required" and
// additionalProperties:false, so optional fields are modelled as empty strings.
func analysisSchema() map[string]any {
	actionItem := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task":  map[string]any{"type": "string"},
			"owner": map[string]any{"type": "string"},
			"due":   map[string]any{"type": "string"},
		},
		"required":             []string{"task", "owner", "due"},
		"additionalProperties": false,
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"title":        map[string]any{"type": "string"},
			"summary":      map[string]any{"type": "string"},
			"key_points":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"action_items": map[string]any{"type": "array", "items": actionItem},
		},
		"required":             []string{"title", "summary", "key_points", "action_items"},
		"additionalProperties": false,
	}
}
