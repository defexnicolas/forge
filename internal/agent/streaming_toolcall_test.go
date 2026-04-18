package agent

import (
	"context"
	"strings"
	"testing"

	"forge/internal/config"
	"forge/internal/llm"
	"forge/internal/tools"
)

// splitChunkProvider streams its first Chat response as a sequence of text
// chunks — one llm.ChatEvent per chunk — so the test can reproduce a tag like
// "<tool_call>" split across the boundary of two network chunks. The
// non-streaming Chat path returns the full concatenation for any fallback.
type splitChunkProvider struct {
	chunks []string
	calls  int
}

func (p *splitChunkProvider) Name() string { return "fake" }
func (p *splitChunkProvider) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	p.calls++
	return &llm.ChatResponse{Content: strings.Join(p.chunks, "")}, nil
}
func (p *splitChunkProvider) Stream(ctx context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	ch := make(chan llm.ChatEvent, len(p.chunks)+1)
	go func() {
		defer close(ch)
		for _, c := range p.chunks {
			ch <- llm.ChatEvent{Type: "text", Text: c}
		}
		ch <- llm.ChatEvent{Type: "done"}
	}()
	p.calls++
	return ch, nil
}
func (p *splitChunkProvider) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	return nil, nil
}
func (p *splitChunkProvider) ProbeModel(ctx context.Context, id string) (*llm.ModelInfo, error) {
	return nil, nil
}
func (p *splitChunkProvider) LoadModel(ctx context.Context, id string, cfg llm.LoadConfig) error {
	return nil
}

// TestStreamResponseToolCallSplitAcrossChunks verifies that the incremental
// tag search in streamResponse still fires when "<tool_call>" arrives split
// across two chunks. The optimization backs off len(tag)-1 bytes on each
// chunk boundary precisely to catch this case — without that back-off we
// would emit a stale delta containing the opening part of a tool-call tag.
func TestStreamResponseToolCallSplitAcrossChunks(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	providers := llm.NewRegistry()
	provider := &splitChunkProvider{chunks: []string{
		"<tool_",
		`call>{"name":"read_file","input":{"path":"x"}}</tool_call>`,
	}}
	providers.Register(provider)

	runtime := newTestRuntime(t, cwd, cfg, registry, providers)
	events := make(chan Event, 64)
	req := llm.ChatRequest{
		Model:    "fake",
		Messages: []llm.Message{{Role: "user", Content: "hi"}},
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _, _ = runtime.streamResponse(context.Background(), provider, req, 1, events)
		close(events)
	}()

	var deltas []string
	for ev := range events {
		if ev.Type == EventAssistantDelta {
			deltas = append(deltas, ev.Text)
		}
	}
	<-done

	// Expected: exactly one delta — the first chunk "<tool_" was emitted
	// before the tag could be completed. The second chunk "call>..." lands
	// after toolCallSeen flips true (the tag is now whole in the accumulated
	// buffer) and must NOT be forwarded to the UI.
	if len(deltas) != 1 || deltas[0] != "<tool_" {
		t.Fatalf("deltas = %#v, want exactly [\"<tool_\"]", deltas)
	}
}

// TestStreamResponseFullToolCallInFirstChunk covers the common case where the
// full "<tool_call>" tag lands in a single chunk. No delta should be emitted
// for that chunk — toolCallSeen must fire the moment the tag appears.
func TestStreamResponseFullToolCallInFirstChunk(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	providers := llm.NewRegistry()
	provider := &splitChunkProvider{chunks: []string{
		`prose here <tool_call>{"name":"noop","input":{}}</tool_call>`,
	}}
	providers.Register(provider)

	runtime := newTestRuntime(t, cwd, cfg, registry, providers)
	events := make(chan Event, 64)
	req := llm.ChatRequest{
		Model:    "fake",
		Messages: []llm.Message{{Role: "user", Content: "hi"}},
	}

	go func() {
		_, _, _, _ = runtime.streamResponse(context.Background(), provider, req, 1, events)
		close(events)
	}()

	var deltas []string
	for ev := range events {
		if ev.Type == EventAssistantDelta {
			deltas = append(deltas, ev.Text)
		}
	}
	if len(deltas) != 0 {
		t.Fatalf("deltas = %#v, want none (tag was present from the first chunk)", deltas)
	}
}
