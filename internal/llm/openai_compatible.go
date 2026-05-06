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
	"sync/atomic"
	"time"

	"forge/internal/config"
)

// Backend identifies which OpenAI-compatible server shape OpenAICompatible is
// talking to. The configured provider name in the registry is unreliable —
// users frequently reuse the "lmstudio" slot with a base_url pointing at
// llama-server. We auto-detect by probing /v1/models response shape.
type Backend int32

const (
	BackendUnknown       Backend = 0
	BackendLMStudio      Backend = 1
	BackendLlamaServer   Backend = 2
	BackendGenericOpenAI Backend = 3
)

type OpenAICompatible struct {
	name   string
	cfg    config.ProviderConfig
	client *http.Client
	// backend caches the detected server kind. Resolved lazily on the first
	// LoadModel/ProbeModel/LoadedModels call. Stored as int32 for atomic CAS.
	backend atomic.Int32
}

func NewOpenAICompatible(name string, cfg config.ProviderConfig) *OpenAICompatible {
	return &OpenAICompatible{
		name: name,
		cfg:  cfg,
		// Do not impose a second hard timeout here. The runtime already wraps
		// requests in a context with the configured request timeout, and a
		// fixed client timeout can fire first and ignore that higher-level
		// configuration.
		client: &http.Client{},
	}
}

// BackendKind returns the cached backend kind, or BackendUnknown if not yet
// resolved. Does not perform I/O.
func (p *OpenAICompatible) BackendKind() Backend {
	return Backend(p.backend.Load())
}

// SupportsExplicitLoad reports whether this provider can honor a programmatic
// LoadModel call. True only for LM Studio. llama-server and generic OpenAI
// compatible endpoints don't expose load endpoints, so attempts would fail
// (and currently get logged as "proceeding anyway"). Callers can pre-check
// this and skip the LoadModel call entirely.
func (p *OpenAICompatible) SupportsExplicitLoad() bool {
	return p.BackendKind() == BackendLMStudio
}

// BackendName returns a stable string identifying the resolved backend shape:
// "lmstudio", "llama-server", "openai", or the configured provider name when
// unresolved. Used by status-bar and label rendering so the UI shows the real
// backend even when the registry slot was reused.
func (p *OpenAICompatible) BackendName() string {
	switch p.BackendKind() {
	case BackendLMStudio:
		return "lmstudio"
	case BackendLlamaServer:
		return "llama-server"
	case BackendGenericOpenAI:
		return "openai"
	default:
		return p.name
	}
}

// resolveBackend probes /v1/models to classify the backend. Cheap because
// every caller is about to need that data anyway. LM Studio populates
// state="loaded" and loaded_context_length on at least one row; llama-server
// returns rows with neither. Caches the result on success; leaves Unknown on
// failure so the next call can retry. Safe to call concurrently — at most one
// extra probe will race, and they'll converge on the same answer.
func (p *OpenAICompatible) resolveBackend(ctx context.Context) Backend {
	if kind := p.BackendKind(); kind != BackendUnknown {
		return kind
	}
	models, err := p.ListModels(ctx)
	if err != nil || len(models) == 0 {
		return BackendUnknown
	}
	return p.classifyAndCache(models)
}

// RefreshBackend invalidates the cached backend kind and re-classifies by
// probing /v1/models. Useful after the user changes which server is listening
// at the configured BaseURL — e.g. they stopped llama-server and started LM
// Studio at the same port without going through Forge's provider form. The
// cached BackendKind would otherwise stay stuck on the previous value, and
// SupportsExplicitLoad / BackendName would lie. /model reload uses this to
// force a fresh detection before deciding what to do.
func (p *OpenAICompatible) RefreshBackend(ctx context.Context) {
	p.backend.Store(int32(BackendUnknown))
	p.resolveBackend(ctx)
}

// classifyAndCache classifies the backend from an already-fetched models slice
// and stores the result. Lets ProbeModel reuse the rows it just retrieved
// instead of forcing a second /v1/models roundtrip via resolveBackend, and
// lets it skip the LMStudio-only /api/v0/models 404 when the rows already
// reveal the backend is something else.
func (p *OpenAICompatible) classifyAndCache(models []ModelInfo) Backend {
	if len(models) == 0 {
		return BackendUnknown
	}
	kind := BackendLlamaServer
	for _, m := range models {
		if strings.TrimSpace(m.State) != "" || m.LoadedContextLength > 0 || m.MaxContextLength > 0 {
			kind = BackendLMStudio
			break
		}
	}
	p.backend.Store(int32(kind))
	return kind
}

// LoadedModels returns the models currently resident on the backend. For LM
// Studio that's the rows with State="loaded"; for llama-server every row in
// /v1/models is by definition the loaded model. Drives the model-multi reuse
// picker without baking LM Studio assumptions into the form.
func (p *OpenAICompatible) LoadedModels(ctx context.Context) ([]ModelInfo, error) {
	models, err := p.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	kind := p.resolveBackend(ctx)
	if kind == BackendLMStudio {
		var loaded []ModelInfo
		for _, m := range models {
			if m.State == "loaded" {
				loaded = append(loaded, m)
			}
		}
		return loaded, nil
	}
	return models, nil
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

	payload := buildChatPayload(req, false)
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
	return p.StreamWithIdle(ctx, req, 0)
}

// StreamWithIdle is Stream with an idle-timeout watchdog. If idle > 0, the
// request is cancelled when no SSE chunk has arrived within `idle` after the
// first chunk was received. The watchdog deliberately does not arm during the
// initial prompt-processing window — local backends like LM Studio can spend
// many minutes processing a 12k-token prompt before emitting any token, and
// killing the request during that window would defeat the purpose.
//
// When the watchdog fires, the error event delivered through the events
// channel is rewritten to ErrIdleTimeout so callers can distinguish "provider
// went silent mid-stream" from a wall-clock context deadline.
func (p *OpenAICompatible) StreamWithIdle(ctx context.Context, req ChatRequest, idle time.Duration) (<-chan ChatEvent, error) {
	if p.cfg.BaseURL == "" {
		return nil, ErrProviderNotConfigured
	}
	if req.Model == "" {
		req.Model = p.cfg.DefaultModel
	}

	payload := buildChatPayload(req, true)
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	// Derived context so the watchdog can cancel the in-flight request
	// without affecting the caller's ctx.
	streamCtx, streamCancel := context.WithCancel(ctx)

	httpReq, err := http.NewRequestWithContext(streamCtx, http.MethodPost, p.endpoint("/chat/completions"), bytes.NewReader(body))
	if err != nil {
		streamCancel()
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
		streamCancel()
		return nil, err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		streamCancel()
		errBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("provider %s returned %s: %s", p.name, resp.Status, string(errBody))
	}

	// lastActivity holds the unix-nano timestamp of the most recent byte
	// read from the response body. Zero means "no chunk yet" — the watchdog
	// treats that as still in prompt-processing and skips the staleness
	// check until the first chunk arrives.
	var lastActivity atomic.Int64
	var idleFired atomic.Bool

	var reader io.Reader = resp.Body
	if idle > 0 {
		reader = &activityReader{r: reader, onRead: func() {
			lastActivity.Store(time.Now().UnixNano())
		}}
	}

	if logPath := strings.TrimSpace(os.Getenv("FORGE_SSE_LOG")); logPath != "" {
		if f, ferr := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); ferr == nil {
			fmt.Fprintf(f, "\n=== %s | %s | model=%s ===\n", time.Now().Format(time.RFC3339Nano), p.name, req.Model)
			reader = io.TeeReader(reader, f)
			// Note: f leaks until process exit; matches the prior behavior
			// where the log file was deferred-closed inside the goroutine.
			// Move the close into the SSE goroutine below.
			go func() {
				<-streamCtx.Done()
				_ = f.Close()
			}()
		}
	}

	rawEvents := make(chan ChatEvent, 16)
	events := make(chan ChatEvent, 16)

	// Watchdog: arms once lastActivity becomes non-zero. Tick interval is
	// idle/4 so we detect within ~25% of the configured window without
	// spinning. Capped at 1s minimum to avoid burning CPU on tiny idles.
	if idle > 0 {
		go func() {
			tick := max(idle/4, time.Second)
			ticker := time.NewTicker(tick)
			defer ticker.Stop()
			for {
				select {
				case <-streamCtx.Done():
					return
				case now := <-ticker.C:
					last := lastActivity.Load()
					if last == 0 {
						continue
					}
					if now.Sub(time.Unix(0, last)) > idle {
						idleFired.Store(true)
						streamCancel()
						return
					}
				}
			}
		}()
	}

	go func() {
		defer close(rawEvents)
		defer resp.Body.Close()
		p.readSSE(reader, rawEvents)
	}()

	go func() {
		defer close(events)
		defer streamCancel()
		for evt := range rawEvents {
			if evt.Type == "error" && idleFired.Load() {
				evt.Error = ErrIdleTimeout
			}
			events <- evt
		}
	}()

	return events, nil
}

// activityReader notifies onRead whenever the underlying reader returns data,
// letting the idle watchdog track per-chunk freshness without touching the
// SSE parser.
type activityReader struct {
	r      io.Reader
	onRead func()
}

func (a *activityReader) Read(p []byte) (int, error) {
	n, err := a.r.Read(p)
	if n > 0 && a.onRead != nil {
		a.onRead()
	}
	return n, err
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
					Content          string          `json:"content"`
					ReasoningContent string          `json:"reasoning_content"`
					ToolCalls        []toolCallDelta `json:"tool_calls"`
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

		if delta.ReasoningContent != "" {
			events <- ChatEvent{Type: "reasoning", Text: delta.ReasoningContent}
		}
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

func buildChatPayload(req ChatRequest, stream bool) map[string]any {
	payload := map[string]any{
		"model":    req.Model,
		"messages": req.Messages,
		"stream":   stream,
	}
	if len(req.Tools) > 0 {
		payload["tools"] = req.Tools
		// tool_choice is only meaningful when tools are advertised. If a
		// caller set ToolChoice without sending Tools, ignore it rather
		// than emit an invalid request body.
		if req.ToolChoice != nil {
			payload["tool_choice"] = req.ToolChoice
		}
	}
	if req.Temperature != nil {
		payload["temperature"] = *req.Temperature
	}
	return payload
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
	// Classify the backend from the rows we already fetched so the next
	// step can skip the LMStudio-only /api/v0/models call when it's
	// guaranteed to 404 (llama-server, generic OpenAI). The first probe
	// previously left the cache Unknown until probeLlamaServerCtx ran,
	// which meant every fresh process logged a one-time 404 in the
	// llama-server console.
	if p.BackendKind() == BackendUnknown {
		p.classifyAndCache(models)
	}
	if !modelsHaveContextMetadata(models) && p.BackendKind() == BackendLMStudio {
		// Older LM Studio builds only expose the enhanced fields on the
		// native /api/v0/models endpoint.
		if enhanced, eerr := p.listModelsEnhanced(ctx); eerr == nil && len(enhanced) > 0 {
			models = enhanced
		}
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("provider %s returned no models", p.name)
	}
	generic := modelID == "" || modelID == "local-model"
	// Pick the right ModelInfo to return (exact ID match, first loaded, then
	// first entry). Then enrich it with backend-specific context metadata
	// before returning.
	var picked *ModelInfo
	if !generic {
		for i := range models {
			if models[i].ID == modelID {
				picked = &models[i]
				break
			}
		}
	}
	if picked == nil {
		for i := range models {
			if models[i].State == "loaded" {
				picked = &models[i]
				break
			}
		}
	}
	if picked == nil {
		picked = &models[0]
	}
	// llama-server doesn't populate loaded_context_length on /v1/models — it
	// only exists in LM Studio's enhanced response. To make the status bar's
	// ctx:N/Mk denominator meaningful, query /props (llama-server's native
	// status endpoint) and pull n_ctx from default_generation_settings. With
	// --parallel K this is the per-slot window, which is exactly what each
	// request gets, so it's the right number to display.
	if picked.LoadedContextLength == 0 && p.resolveBackend(ctx) == BackendLlamaServer {
		if n := p.probeLlamaServerCtx(ctx); n > 0 {
			picked.LoadedContextLength = n
			if picked.MaxContextLength == 0 {
				picked.MaxContextLength = n
			}
		}
	}
	return picked, nil
}

// probeLlamaServerCtx fetches /props from llama-server and returns the
// per-slot context window in tokens. Returns 0 on any failure — the caller
// must treat this as best-effort enrichment, not a hard requirement.
//
// Response shape (as of llama.cpp 2024+):
//
//	{ "default_generation_settings": { "n_ctx": 32768 }, ... }
//
// Older builds put n_ctx at the top level; we accept both spellings.
func (p *OpenAICompatible) probeLlamaServerCtx(ctx context.Context) int {
	base := strings.TrimRight(p.cfg.BaseURL, "/")
	stripped := strings.TrimSuffix(base, "/v1")
	url := stripped + "/props"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0
	}
	if apiKey := p.apiKey(); apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0
	}
	var decoded struct {
		DefaultGenerationSettings struct {
			NCtx int `json:"n_ctx"`
		} `json:"default_generation_settings"`
		NCtx int `json:"n_ctx"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return 0
	}
	if decoded.DefaultGenerationSettings.NCtx > 0 {
		return decoded.DefaultGenerationSettings.NCtx
	}
	return decoded.NCtx
}

// LoadModel asks LM Studio to load the given model with custom context length
// (and flash attention). Tries the REST endpoint first, falls back to the
// `lms` CLI when REST isn't available (older LM Studio builds or builds that
// don't expose the load endpoint). Returns ErrNotSupported for providers that
// don't look like LM Studio.
func (p *OpenAICompatible) LoadModel(ctx context.Context, modelID string, loadCfg LoadConfig) error {
	if p.cfg.BaseURL == "" {
		return ErrProviderNotConfigured
	}
	if modelID == "" {
		return fmt.Errorf("model id is required")
	}
	// Resolve the backend before deciding to issue load endpoints. llama-server
	// and generic OpenAI-compatible providers don't have programmatic load —
	// trying anyway produces noise (HTTP 404 → lms CLI not found) without
	// changing anything observable.
	if p.resolveBackend(ctx) != BackendLMStudio {
		return ErrNotSupported
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
	// LM Studio 0.3+ moved the load-time options under a nested "config"
	// object; older builds accepted them at the top level. Mirror the same
	// fields in both places so either version picks them up. Unknown fields
	// are ignored by the server.
	nested := map[string]any{}
	if loadCfg.ContextLength > 0 {
		nested["context_length"] = loadCfg.ContextLength
		nested["contextLength"] = loadCfg.ContextLength
	}
	if loadCfg.FlashAttention {
		nested["flash_attention"] = true
		nested["flashAttention"] = true
	}
	if loadCfg.ParallelSlots > 0 {
		// Field names have shifted across LM Studio versions (and inside
		// llama.cpp vs mlx backends). Send every known spelling so at
		// least one is honored. If the user still sees 1 GEN slot after
		// this reload, the field name changed again and we need to log
		// the echoed response to find the new one.
		nested["max_parallel_sequences"] = loadCfg.ParallelSlots
		nested["maxParallelSequences"] = loadCfg.ParallelSlots
		nested["parallel_requests"] = loadCfg.ParallelSlots
		nested["parallelRequests"] = loadCfg.ParallelSlots
		nested["n_parallel"] = loadCfg.ParallelSlots
		nested["nParallel"] = loadCfg.ParallelSlots
		nested["num_parallel"] = loadCfg.ParallelSlots
		nested["numParallel"] = loadCfg.ParallelSlots
	}
	body := map[string]any{
		"model":            modelID,
		"identifier":       modelID,
		"echo_load_config": true,
		"config":           nested,
		"load_config":      nested,
	}
	for k, v := range nested {
		body[k] = v
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
		// Surface the full echoed config to stderr when ParallelSlots was
		// requested. LM Studio's actual slot field has moved between
		// versions and spellings — logging the echo lets the user (and
		// future code) see exactly which field LM Studio accepted and
		// whether it matches the requested value.
		if loadCfg.ParallelSlots > 0 && len(respBody) > 0 {
			fmt.Fprintf(os.Stderr, "lm-studio load echo [%s]: %s\n", path, strings.TrimSpace(string(respBody)))
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
