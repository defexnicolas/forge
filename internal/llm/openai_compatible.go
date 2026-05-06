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
//
// LM Studio is detected in two passes because older builds don't expose
// state / loaded_context_length on /v1/models — only on the native
// /api/v0/models endpoint. Without the second pass an LM Studio install
// gets misclassified as llama-server, which then makes /model reload
// refuse to run because SupportsExplicitLoad returns false.
func (p *OpenAICompatible) resolveBackend(ctx context.Context) Backend {
	if kind := p.BackendKind(); kind != BackendUnknown {
		return kind
	}
	models, err := p.ListModels(ctx)
	if err != nil || len(models) == 0 {
		return BackendUnknown
	}
	if kind := p.classifyAndCache(models); kind == BackendLMStudio {
		return kind
	}
	// Pass 2: the rows from /v1/models look generic. Try LM Studio's
	// extended /api/v0/models — if it answers 200, the BaseURL is
	// definitely LM Studio (older build that hides those fields on /v1).
	if enhanced, eerr := p.listModelsEnhanced(ctx); eerr == nil && len(enhanced) > 0 {
		p.backend.Store(int32(BackendLMStudio))
		return BackendLMStudio
	}
	// Confirmed not LM Studio. Cache as llama-server (the most common
	// alternative; downstream code only branches on "is LM Studio or
	// not", so OpenAI-compatible non-LM-Studio endpoints are handled
	// the same way as llama-server).
	p.backend.Store(int32(BackendLlamaServer))
	return BackendLlamaServer
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
// and stores the result IF the rows clearly identify LM Studio (state field
// or context-length fields populated). When the rows are generic the
// function returns BackendUnknown without caching — the caller is expected
// to call resolveBackend (which falls back to /api/v0/models) before
// committing to "this is llama-server". Premature classification was
// misclassifying older LM Studio builds whose /v1/models response doesn't
// carry the extended fields.
func (p *OpenAICompatible) classifyAndCache(models []ModelInfo) Backend {
	if len(models) == 0 {
		return BackendUnknown
	}
	for _, m := range models {
		if strings.TrimSpace(m.State) != "" || m.LoadedContextLength > 0 || m.MaxContextLength > 0 {
			p.backend.Store(int32(BackendLMStudio))
			return BackendLMStudio
		}
	}
	return BackendUnknown
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
	// Two-pass backend detection. classifyAndCache only commits to
	// BackendLMStudio when /v1/models clearly says so (state or context
	// fields populated). When the rows look generic we still try
	// /api/v0/models — older LM Studio builds hide the metadata there.
	// If neither pass reveals LM Studio, settle on BackendLlamaServer so
	// SupportsExplicitLoad returns false and downstream behaves correctly.
	if p.BackendKind() == BackendUnknown {
		if classified := p.classifyAndCache(models); classified == BackendUnknown {
			if enhanced, eerr := p.listModelsEnhanced(ctx); eerr == nil && len(enhanced) > 0 {
				models = enhanced
				p.backend.Store(int32(BackendLMStudio))
			} else {
				p.backend.Store(int32(BackendLlamaServer))
			}
		}
	} else if !modelsHaveContextMetadata(models) && p.BackendKind() == BackendLMStudio {
		// Cached as LM Studio but this particular response is missing
		// the extended fields. Try /api/v0/models so the picked
		// ModelInfo carries loaded_context_length.
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
	// Try it first, fall back to /api/v0/models/load for older builds.
	stripped := strings.TrimSuffix(base, "/v1")
	paths := []string{"/api/v1/models/load", "/api/v0/models/load"}
	// Recent LM Studio versions reject any unknown key with HTTP 400
	// "unrecognized_keys" — including camelCase aliases, "config",
	// "load_config", "identifier", and every parallel-slots variant we
	// used to fan out. The earlier shotgun approach worked when LM Studio
	// silently ignored extras; now it fails the load entirely.
	//
	// Send the minimal, snake_case-only payload that's documented and
	// accepted across versions. ParallelSlots is intentionally not sent
	// over REST: every spelling in our previous fan-out is now rejected,
	// and there's no documented v1 field for it. Configure parallel
	// generation slots inside LM Studio's UI / lms CLI — Forge's chat
	// completions still benefit from whatever slot count the server has
	// resident.
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
		// Treat any 4xx as path-recoverable so a 400 from /api/v1 (newer
		// LM Studio with strict schema) falls through to /api/v0 instead
		// of aborting the whole load. 5xx still aborts because retrying
		// the same payload against the older endpoint won't help.
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			lastErr = fmt.Errorf("%s %s: %s", path, resp.Status, strings.TrimSpace(string(respBody)))
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
