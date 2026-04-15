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
	"os/exec"
	"strconv"
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
			Usage   *TokenUsage `json:"usage,omitempty"`
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
		if chunk.Usage != nil {
			events <- ChatEvent{Type: "usage", Usage: chunk.Usage}
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
	Index    int    `json:"index"`
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

// ProbeModel queries the provider for metadata about the given model.
// For LM Studio, it prefers the enhanced /api/v0/models endpoint which exposes
// loaded_context_length and max_context_length. Falls back to /v1/models for
// plain OpenAI-compatible providers.
//
// If modelID is empty or the generic "local-model", it returns the first
// loaded model (LM Studio reports state="loaded" for the active model).
func (p *OpenAICompatible) ProbeModel(ctx context.Context, modelID string) (*ModelInfo, error) {
	if p.cfg.BaseURL == "" {
		return nil, ErrProviderNotConfigured
	}
	// Try standard /v1/models first — recent LM Studio versions already
	// expose state / loaded_context_length / max_context_length there, and
	// it avoids a guaranteed 404 roundtrip for providers that only speak /v1.
	models, err := p.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	if !modelsHaveContextMetadata(models) {
		// Older LM Studio builds only expose the enhanced fields on the
		// native /api/v0/models endpoint. Try it opportunistically.
		if enhanced, eerr := p.listModelsEnhanced(ctx); eerr == nil && len(enhanced) > 0 {
			models = enhanced
		}
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("provider %s returned no models", p.name)
	}
	generic := modelID == "" || modelID == "local-model"
	// First pass: exact ID match.
	if !generic {
		for i := range models {
			if models[i].ID == modelID {
				return &models[i], nil
			}
		}
	}
	// Second pass: first loaded model.
	for i := range models {
		if models[i].State == "loaded" {
			return &models[i], nil
		}
	}
	// Fallback: first entry.
	return &models[0], nil
}

// LoadModel asks LM Studio to load the given model with custom context length
// (and flash attention). Tries the REST endpoint first, falls back to the
// `lms` CLI when REST isn't available (older LM Studio builds or builds that
// don't expose the load endpoint). Returns ErrNotSupported for providers that
// don't look like LM Studio (no /api/v0 prefix derivable).
func (p *OpenAICompatible) LoadModel(ctx context.Context, modelID string, loadCfg LoadConfig) error {
	if p.cfg.BaseURL == "" {
		return ErrProviderNotConfigured
	}
	if modelID == "" {
		return fmt.Errorf("model id is required")
	}
	base := strings.TrimRight(p.cfg.BaseURL, "/")
	if !strings.HasSuffix(base, "/v1") {
		return ErrNotSupported
	}

	httpErr := p.loadModelHTTP(ctx, base, modelID, loadCfg)
	if httpErr == nil {
		return nil
	}
	cliErr := p.loadModelCLI(ctx, modelID, loadCfg)
	if cliErr == nil {
		return nil
	}
	return fmt.Errorf("load failed via HTTP (%v) and via lms CLI (%v)", httpErr, cliErr)
}

func (p *OpenAICompatible) loadModelHTTP(ctx context.Context, base, modelID string, loadCfg LoadConfig) error {
	// LM Studio's documented native load endpoint is /api/v1/models/load.
	// We try it first, then fall back to /api/v0/models/load for older builds.
	stripped := strings.TrimSuffix(base, "/v1")
	paths := []string{"/api/v1/models/load", "/api/v0/models/load"}
	body := map[string]any{
		"model":            modelID,
		"echo_load_config": true,
	}
	if loadCfg.ContextLength > 0 {
		body["context_length"] = loadCfg.ContextLength
	}
	if loadCfg.FlashAttention {
		body["flash_attention"] = true
	}
	// LM Studio has used several field names for parallel generation slots
	// across versions. Send all three — unknown fields are ignored — so
	// "max_parallel_sequences=2" produces the 2+ GEN slots the user wants.
	if loadCfg.ParallelSlots > 0 {
		body["max_parallel_sequences"] = loadCfg.ParallelSlots
		body["parallel_requests"] = loadCfg.ParallelSlots
		body["n_parallel"] = loadCfg.ParallelSlots
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}

	var lastErr error
	for _, path := range paths {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, stripped+path, bytes.NewReader(payload))
		if err != nil {
			lastErr = err
			continue
		}
		httpReq.Header.Set("Content-Type", "application/json")
		if apiKey := p.apiKey(); apiKey != "" {
			httpReq.Header.Set("Authorization", "Bearer "+apiKey)
		}
		client := &http.Client{Timeout: 5 * time.Minute}
		resp, err := client.Do(httpReq)
		if err != nil {
			lastErr = err
			continue
		}
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == 404 {
			lastErr = fmt.Errorf("%s: 404", path)
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("%s %s: %s", path, resp.Status, strings.TrimSpace(string(respBody)))
		}
		// Validate echoed config so silent no-ops surface as errors.
		if loadCfg.ContextLength > 0 {
			var echo struct {
				LoadConfig struct {
					ContextLength int `json:"context_length"`
				} `json:"load_config"`
			}
			if jerr := json.Unmarshal(respBody, &echo); jerr == nil && echo.LoadConfig.ContextLength > 0 {
				if echo.LoadConfig.ContextLength != loadCfg.ContextLength {
					return fmt.Errorf("LM Studio applied context_length=%d (requested %d) — model may not support that window",
						echo.LoadConfig.ContextLength, loadCfg.ContextLength)
				}
			}
		}
		return nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no load endpoint reachable")
	}
	return lastErr
}

func (p *OpenAICompatible) loadModelCLI(ctx context.Context, modelID string, loadCfg LoadConfig) error {
	bin := os.Getenv("FORGE_LMS_BIN")
	if bin == "" {
		bin = "lms"
	}
	args := []string{"load", modelID}
	if loadCfg.ContextLength > 0 {
		args = append(args, "--context-length", strconv.Itoa(loadCfg.ContextLength))
	}
	args = append(args, "--gpu", "max")
	cliCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(cliCtx, bin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %v (%s)", bin, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func modelsHaveContextMetadata(models []ModelInfo) bool {
	for _, m := range models {
		if m.LoadedContextLength > 0 || m.MaxContextLength > 0 {
			return true
		}
	}
	return false
}

// listModelsEnhanced hits LM Studio's /api/v0/models (OpenAI-compatible with
// extended metadata). Derives the URL from BaseURL by replacing a trailing /v1
// segment with /api/v0. Returns error or empty slice when unavailable.
func (p *OpenAICompatible) listModelsEnhanced(ctx context.Context) ([]ModelInfo, error) {
	base := strings.TrimRight(p.cfg.BaseURL, "/")
	if !strings.HasSuffix(base, "/v1") {
		return nil, fmt.Errorf("enhanced endpoint not applicable")
	}
	enhanced := strings.TrimSuffix(base, "/v1") + "/api/v0/models"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, enhanced, nil)
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
		return nil, fmt.Errorf("enhanced probe %s: %s", p.name, resp.Status)
	}
	var decoded struct {
		Data []ModelInfo `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, err
	}
	return decoded.Data, nil
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
