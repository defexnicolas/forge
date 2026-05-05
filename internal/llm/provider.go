package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"strings"
	"sync"
	"syscall"
)

type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ToolDef struct {
	Type     string      `json:"type"`
	Function FunctionDef `json:"function"`
}

type FunctionDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type ChatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Tools       []ToolDef `json:"tools,omitempty"`
	Temperature *float64  `json:"temperature,omitempty"`
	// ToolChoice is the OpenAI-shaped tool_choice override. Accepts
	// "none" / "auto" / "required" (as a string) or a specific function
	// pin like {"type":"function","function":{"name":"whatsapp_send"}}.
	// Leave nil to let the provider default (auto when tools are
	// present). Used by the bailout-retry paths that need to FORCE a
	// specific tool call when the model has been observed inventing
	// excuses for tools it actually has — every other call site leaves
	// this nil and behaves exactly as before.
	ToolChoice any `json:"-"`
}

type ChatResponse struct {
	Model     string     `json:"model"`
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

type TokenUsage struct {
	PromptTokens     int `json:"prompt_tokens,omitempty"`
	CompletionTokens int `json:"completion_tokens,omitempty"`
	TotalTokens      int `json:"total_tokens,omitempty"`
}

type ChatEvent struct {
	Type      string
	Text      string
	ToolCalls []ToolCall
	Usage     *TokenUsage
	Error     error
}

type ModelInfo struct {
	ID                  string `json:"id"`
	State               string `json:"state,omitempty"`
	MaxContextLength    int    `json:"max_context_length,omitempty"`
	LoadedContextLength int    `json:"loaded_context_length,omitempty"`
	Arch                string `json:"arch,omitempty"`
	Quantization        string `json:"quantization,omitempty"`
}

type LoadConfig struct {
	ContextLength  int
	FlashAttention bool
	// ParallelSlots tells the backend (LM Studio) how many concurrent
	// generation slots to reserve for this model. 0 means leave the backend
	// default; >=2 enables parallel decoding so the agent can fan out work
	// (e.g. parallel tool calls, /btw, subagents) without queueing.
	ParallelSlots int
}

type Provider interface {
	Name() string
	Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
	Stream(ctx context.Context, req ChatRequest) (<-chan ChatEvent, error)
	ListModels(ctx context.Context) ([]ModelInfo, error)
	ProbeModel(ctx context.Context, modelID string) (*ModelInfo, error)
	LoadModel(ctx context.Context, modelID string, cfg LoadConfig) error
}

type Registry struct {
	mu        sync.RWMutex
	providers map[string]Provider
}

func NewRegistry() *Registry {
	return &Registry{providers: map[string]Provider{}}
}

func (r *Registry) Register(provider Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[provider.Name()] = provider
}

func (r *Registry) Get(name string) (Provider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	provider, ok := r.providers[name]
	return provider, ok
}

func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.providers))
	for name := range r.providers {
		names = append(names, name)
	}
	return names
}

var ErrProviderNotConfigured = errors.New("provider is not configured")
var ErrNotSupported = errors.New("operation not supported by this provider")

// ErrIdleTimeout is returned when a streaming request is cancelled because no
// SSE chunk arrived within the configured idle window. It is intentionally
// distinct from context.DeadlineExceeded so callers can tell "provider went
// silent" apart from "wall-clock deadline reached" in logs and metrics, while
// still being classified as a provider timeout for retry purposes.
var ErrIdleTimeout = errors.New("provider idle timeout: no chunk received within idle window")

func IsProviderTimeout(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrIdleTimeout) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline exceeded")
}

func IsProviderUnavailable(err error) bool {
	if err == nil || IsProviderTimeout(err) {
		return false
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		err = urlErr.Err
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		if errors.Is(opErr.Err, syscall.ECONNREFUSED) {
			return true
		}
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "actively refused") ||
		strings.Contains(msg, "no such host") ||
		strings.Contains(msg, "server misbehaving")
}

func IsToolCallingUnsupported(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	hasToolHint := strings.Contains(msg, "tools") ||
		strings.Contains(msg, "tool_calls") ||
		strings.Contains(msg, "tool calls") ||
		strings.Contains(msg, "function calling") ||
		strings.Contains(msg, "functions")
	if !hasToolHint {
		return false
	}
	return strings.Contains(msg, "unsupported") ||
		strings.Contains(msg, "not supported") ||
		strings.Contains(msg, "does not support") ||
		strings.Contains(msg, "unknown field") ||
		strings.Contains(msg, "invalid field") ||
		strings.Contains(msg, "unrecognized field")
}
