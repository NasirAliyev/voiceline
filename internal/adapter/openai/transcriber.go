package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"

	"github.com/nasiraliev/voiceline/internal/domain"
)

const (
	transcriptionPath = "/audio/transcriptions"
	fieldFile         = "file"
	fieldModel        = "model"
	fallbackAudioName = "audio"
	opTranscription   = "transcription"
)

// Transcriber implements domain.Transcriber against the OpenAI audio
// transcription endpoint.
type Transcriber struct {
	client *Client
	model  string
}

// NewTranscriber builds a transcriber using the given client and model.
func NewTranscriber(client *Client, model string) *Transcriber {
	return &Transcriber{client: client, model: model}
}

// Transcribe uploads the audio and returns its transcript. The multipart body is
// rebuilt from a freshly opened stream on every attempt, so a retry always
// resends the full audio rather than a drained reader.
func (t *Transcriber) Transcribe(ctx context.Context, open domain.AudioOpener, meta domain.AudioMeta) (domain.Transcript, error) {
	build := func(ctx context.Context) (*http.Request, error) {
		return t.buildRequest(ctx, open, meta)
	}

	respBody, err := t.client.do(ctx, opTranscription, build)
	if err != nil {
		return domain.Transcript{}, err
	}

	var parsed struct {
		Text     string `json:"text"`
		Language string `json:"language"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return domain.Transcript{}, fmt.Errorf("decode transcription response: %w", err)
	}

	// An empty transcript (e.g. silent audio) is a processing failure, not a
	// silent success — the constructor enforces that invariant.
	transcript, err := domain.NewTranscript(parsed.Text, parsed.Language)
	if err != nil {
		return domain.Transcript{}, fmt.Errorf("empty transcription: %w", err)
	}
	return transcript, nil
}

// buildRequest assembles a fresh multipart upload for a single attempt.
func (t *Transcriber) buildRequest(ctx context.Context, open domain.AudioOpener, meta domain.AudioMeta) (*http.Request, error) {
	audio, err := open()
	if err != nil {
		return nil, fmt.Errorf("open audio: %w", err)
	}
	defer audio.Close()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	name := meta.Filename
	if name == "" {
		name = fallbackAudioName
	}
	part, err := writer.CreateFormFile(fieldFile, filepath.Base(name))
	if err != nil {
		return nil, fmt.Errorf("create form file: %w", err)
	}
	if _, err := io.Copy(part, audio); err != nil {
		return nil, fmt.Errorf("copy audio: %w", err)
	}
	if err := writer.WriteField(fieldModel, t.model); err != nil {
		return nil, fmt.Errorf("write model field: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("finalize multipart body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.client.baseURL+transcriptionPath, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set(headerContentType, writer.FormDataContentType())
	return req, nil
}
