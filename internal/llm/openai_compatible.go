package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"forge/internal/config"
)

type OpenAICompatible struct {
	name   string
	cfg    config.ProviderConfig
	client *http.Client
}

func NewOpenAICompatible(name string, cfg config.ProviderConfig) *OpenAICompatible {
	return &OpenAICompatible{
		name: name,
		cfg:  cfg,
		client: &http.Client{
			Timeout: 90 * time.Second,
		},
	}
}

func (p *OpenAICompatible) Name() string {
	return p.name
}

func (p *OpenAICompatible) SupportsTools() bool {
	return p.cfg.SupportsTools
}

func (p *OpenAICompatible) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	if p.cfg.BaseURL == "" {
		return nil, ErrProviderNotConfigured
	}
	if req.Model == "" {
		req.Model = p.cfg.DefaultModel
	}

	payload := map[string]any{
		"model":       req.Model,
		"messages":    req.Messages,
		"temperature": req.Temperature,
		"stream":      false,
	}
	if len(req.Tools) > 0 {
		payload["tools"] = req.Tools
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint("/chat/completions"), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey := p.apiKey(); apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("provider %s returned %s: %s", p.name, resp.Status, string(respBody))
	}

	var decoded struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content   string     `json:"content"`
				ToolCalls []ToolCall `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return nil, err
	}
	if len(decoded.Choices) == 0 {
		return nil, fmt.Errorf("provider %s returned no choices", p.name)
	}
	return &ChatResponse{
		Model:     decoded.Model,
		Content:   decoded.Choices[0].Message.Content,
		ToolCalls: decoded.Choices[0].Message.ToolCalls,
	}, nil
}

func (p *OpenAICompatible) Stream(ctx context.Context, req ChatRequest) (<-chan ChatEvent, error) {
	if p.cfg.BaseURL == "" {
		return nil, ErrProviderNotConfigured
	}
	if req.Model == "" {
		req.Model = p.cfg.DefaultModel
	}

	payload := map[string]any{
		"model":       req.Model,
		"messages":    req.Messages,
		"temperature": req.Temperature,
		"stream":      true,
	}
	if len(req.Tools) > 0 {
		payload["tools"] = req.Tools
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint("/chat/completions"), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if apiKey := p.apiKey(); apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}

	// Use a client without timeout for streaming — rely on context cancellation.
	streamClient := &http.Client{}
	resp, err := streamClient.Do(httpReq)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		errBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("provider %s returned %s: %s", p.name, resp.Status, string(errBody))
	}

	events := make(chan ChatEvent, 16)
	go func() {
		defer close(events)
		defer resp.Body.Close()
		p.readSSE(resp.Body, events)
	}()
	return events, nil
}

func (p *OpenAICompatible) readSSE(body io.Reader, events chan<- ChatEvent) {
	scanner := bufio.NewScanner(body)
	// Allow large lines for tool call arguments.
	scanner.Buffer(make([]byte, 0, 64*1024), 512*1024)

	// Accumulated tool calls from deltas.
	var toolCalls []ToolCall

	for scanner.Scan() {
		line := scanner.Text()

		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		data = strings.TrimSpace(data)

		if data == "[DONE]" {
			if len(toolCalls) > 0 {
				events <- ChatEvent{Type: "tool_calls", ToolCalls: toolCalls}
			}
			events <- ChatEvent{Type: "done"}
			return
		}

		var chunk struct {
			Choices []struct {
				Delta struct {
					Content   string          `json:"content"`
					ToolCalls []toolCallDelta `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue // skip malformed chunks
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		delta := chunk.Choices[0].Delta

		if delta.Content != "" {
			events <- ChatEvent{Type: "text", Text: delta.Content}
		}

		for _, tcd := range delta.ToolCalls {
			toolCalls = mergeToolCallDelta(toolCalls, tcd)
		}
	}

	if err := scanner.Err(); err != nil {
		events <- ChatEvent{Type: "error", Error: err}
		return
	}
	// Stream ended without [DONE] — emit accumulated tool calls if any.
	if len(toolCalls) > 0 {
		events <- ChatEvent{Type: "tool_calls", ToolCalls: toolCalls}
	}
	events <- ChatEvent{Type: "done"}
}

// toolCallDelta represents the incremental chunks for tool calls in SSE.
type toolCallDelta struct {
	Index    int `json:"index"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}

// mergeToolCallDelta accumulates tool call deltas into complete ToolCall objects.
func mergeToolCallDelta(calls []ToolCall, delta toolCallDelta) []ToolCall {
	for delta.Index >= len(calls) {
		calls = append(calls, ToolCall{Type: "function"})
	}
	tc := &calls[delta.Index]
	if delta.ID != "" {
		tc.ID = delta.ID
	}
	if delta.Type != "" {
		tc.Type = delta.Type
	}
	if delta.Function.Name != "" {
		tc.Function.Name += delta.Function.Name
	}
	if delta.Function.Arguments != "" {
		tc.Function.Arguments += delta.Function.Arguments
	}
	return calls
}

func (p *OpenAICompatible) ListModels(ctx context.Context) ([]ModelInfo, error) {
	if p.cfg.BaseURL == "" {
		return nil, ErrProviderNotConfigured
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, p.endpoint("/models"), nil)
	if err != nil {
		return nil, err
	}
	if apiKey := p.apiKey(); apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("provider %s returned %s: %s", p.name, resp.Status, string(body))
	}
	var decoded struct {
		Data []ModelInfo `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, err
	}
	return decoded.Data, nil
}

func (p *OpenAICompatible) endpoint(path string) string {
	return strings.TrimRight(p.cfg.BaseURL, "/") + path
}

func (p *OpenAICompatible) apiKey() string {
	if p.cfg.APIKey != "" {
		return p.cfg.APIKey
	}
	if p.cfg.APIKeyEnv != "" {
		return os.Getenv(p.cfg.APIKeyEnv)
	}
	return ""
}
