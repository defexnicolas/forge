package claw

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"forge/internal/llm"
	"forge/internal/tools"
)

// TestClawToolDefsExposesWebSearchAndFetch documents Claw's exact tool
// surface area: only web_search and web_fetch, never any mutating or
// workspace-scoped tool. A regression here would silently widen Claw's
// blast radius beyond what the user opted into.
func TestClawToolDefsExposesWebSearchAndFetch(t *testing.T) {
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	defs := clawToolDefs(registry)
	got := map[string]bool{}
	for _, d := range defs {
		got[d.Function.Name] = true
	}
	for _, want := range []string{"web_search", "web_fetch"} {
		if !got[want] {
			t.Errorf("expected Claw tool def %q, got %v", want, got)
		}
	}
	// whatsapp_send is allowed but the registry under test was built
	// from RegisterBuiltins which does NOT register whatsapp_send (that
	// happens in workspace.go after Claw is up). So we only assert
	// banning here, not presence.
	for _, banned := range []string{"read_file", "write_file", "edit_file", "apply_patch", "run_command", "execute_task", "spawn_subagent"} {
		if got[banned] {
			t.Errorf("Claw must not expose %q to the LLM", banned)
		}
	}
}

// TestAllowedClawToolNamesIncludesWhatsAppSend documents the policy
// explicitly so a future refactor of the whitelist file does not
// silently drop whatsapp_send (which would orphan the closure
// registered in workspace.go).
func TestAllowedClawToolNamesIncludesWhatsAppSend(t *testing.T) {
	for _, want := range []string{"web_search", "web_fetch", "whatsapp_send"} {
		if !clawToolNamesAllowed(want) {
			t.Errorf("allowedClawToolNames missing %q", want)
		}
	}
}

// TestRunClawChatWithToolsHonoursToolCallLoop verifies that when the
// provider returns a ToolCall, the dispatcher runs it, appends the result
// to the conversation, and re-queries the provider — and that the final
// tool-free message becomes the returned reply.
func TestRunClawChatWithToolsHonoursToolCallLoop(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(stubFetchTool{}, "")

	provider := &scriptedProvider{
		responses: []*llm.ChatResponse{
			// First response: ask for a tool call.
			{
				Content: "thinking...",
				ToolCalls: []llm.ToolCall{{
					ID:   "c1",
					Type: "function",
					Function: llm.FunctionCall{
						Name:      "web_fetch",
						Arguments: `{"url":"https://example.com"}`,
					},
				}},
			},
			// Second response: now that we have the tool result, answer.
			{Content: "The page says hello."},
		},
	}

	temp := 0.4
	reply, err := runClawChatWithTools(context.Background(), provider, "test-model", registry, []llm.Message{
		{Role: "system", Content: "be claw"},
		{Role: "user", Content: "what does example.com say?"},
	}, &temp, true)
	if err != nil {
		t.Fatalf("runClawChatWithTools: %v", err)
	}
	if reply != "The page says hello." {
		t.Errorf("final reply = %q, want 'The page says hello.'", reply)
	}
	if provider.calls != 2 {
		t.Errorf("provider.Chat called %d times, want 2 (tool round + final)", provider.calls)
	}
	// The second request must have included the tool result message so the
	// model could reason about it.
	if len(provider.lastReq.Messages) < 4 {
		t.Fatalf("expected >=4 messages on second request, got %d: %#v", len(provider.lastReq.Messages), provider.lastReq.Messages)
	}
	last := provider.lastReq.Messages[len(provider.lastReq.Messages)-1]
	if last.Role != "tool" || last.ToolCallID != "c1" {
		t.Errorf("last message should be the tool result for c1, got %#v", last)
	}
	if !strings.Contains(last.Content, "stubbed body") {
		t.Errorf("tool result content lost: %q", last.Content)
	}
}

// TestRunClawChatWithToolsSkipsToolsWhenDisabled is the regression that
// keeps over-eager local models from burning the user's web_search
// quota on every chitchat turn. With toolsEnabled=false, the loop
// short-circuits to a single tool-less Chat call no matter how loudly
// the model would have asked for tools.
func TestRunClawChatWithToolsSkipsToolsWhenDisabled(t *testing.T) {
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	provider := &scriptedProvider{
		responses: []*llm.ChatResponse{
			// If the loop respects the disabled flag, it'll only ever
			// pull this one response and never see the second.
			{Content: "Hello, just chatting."},
			{Content: "this should never run"},
		},
	}
	reply, err := runClawChatWithTools(context.Background(), provider, "m", registry, []llm.Message{
		{Role: "user", Content: "hi"},
	}, nil, false)
	if err != nil {
		t.Fatalf("runClawChatWithTools: %v", err)
	}
	if reply != "Hello, just chatting." {
		t.Errorf("reply = %q, want plain greeting", reply)
	}
	if provider.calls != 1 {
		t.Errorf("provider.Chat called %d times, want exactly 1 (no tool loop)", provider.calls)
	}
	if len(provider.lastReq.Tools) != 0 {
		t.Errorf("lastReq.Tools = %v, want empty (tools disabled)", provider.lastReq.Tools)
	}
}

// TestRunClawChatWithToolsSynthesizesFinalAnswerAfterCap exercises the
// dead-end fix: when the model keeps requesting tools past the iteration
// cap, the loop forces one tool-less Chat to extract a synthesis. Without
// this fix Claw would dump "(stopped after N rounds)" and the user would
// see nothing useful from the work just done.
func TestRunClawChatWithToolsSynthesizesFinalAnswerAfterCap(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(stubFetchTool{}, "")

	// Build clawMaxToolIterations consecutive responses that each ask for
	// another web_fetch — the loop should never get a tool-free answer.
	// After the cap, the loop issues one more Chat (tool-less) and that
	// one returns the synthesis below.
	toolRound := &llm.ChatResponse{
		Content: "still investigating",
		ToolCalls: []llm.ToolCall{{
			ID:   "x",
			Type: "function",
			Function: llm.FunctionCall{
				Name:      "web_fetch",
				Arguments: `{"url":"https://example.com"}`,
			},
		}},
	}
	responses := make([]*llm.ChatResponse, 0, clawMaxToolIterations+1)
	for i := 0; i < clawMaxToolIterations; i++ {
		responses = append(responses, toolRound)
	}
	responses = append(responses, &llm.ChatResponse{Content: "Synthesized answer."})

	provider := &scriptedProvider{responses: responses}
	reply, err := runClawChatWithTools(context.Background(), provider, "m", registry, []llm.Message{
		{Role: "user", Content: "what does example.com say?"},
	}, nil, true)
	if err != nil {
		t.Fatalf("runClawChatWithTools: %v", err)
	}
	if reply != "Synthesized answer." {
		t.Errorf("expected synthesized answer, got %q", reply)
	}
	if provider.calls != clawMaxToolIterations+1 {
		t.Errorf("provider.Chat called %d times, want %d (cap rounds + final synthesis)", provider.calls, clawMaxToolIterations+1)
	}
	// The final request must NOT include Tools — that is the whole point
	// of the synthesis pass.
	if len(provider.lastReq.Tools) != 0 {
		t.Errorf("synthesis request must omit Tools, got %d", len(provider.lastReq.Tools))
	}
	// And the synthesis prompt must be the last user message.
	last := provider.lastReq.Messages[len(provider.lastReq.Messages)-1]
	if last.Role != "user" || !strings.Contains(last.Content, "Stop calling tools") {
		t.Errorf("expected synthesis user prompt as last message, got %#v", last)
	}
}

func TestRunClawChatWithToolsRejectsNonAllowedTool(t *testing.T) {
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	provider := &scriptedProvider{
		responses: []*llm.ChatResponse{
			{
				Content: "",
				ToolCalls: []llm.ToolCall{{
					ID: "x",
					Function: llm.FunctionCall{
						Name:      "write_file",
						Arguments: `{"path":"x","content":"y"}`,
					},
				}},
			},
			{Content: "asked write_file but it was denied; no edits made"},
		},
	}
	reply, err := runClawChatWithTools(context.Background(), provider, "m", registry, []llm.Message{
		{Role: "user", Content: "edit a file"},
	}, nil, true)
	if err != nil {
		t.Fatalf("runClawChatWithTools: %v", err)
	}
	// The dispatcher returned an error string for the denied tool — the
	// model decided to acknowledge it. Both must be true.
	if !strings.Contains(provider.lastReq.Messages[len(provider.lastReq.Messages)-1].Content, "tool not allowed") {
		t.Errorf("expected tool result to say 'tool not allowed', got: %q", provider.lastReq.Messages[len(provider.lastReq.Messages)-1].Content)
	}
	if reply == "" {
		t.Error("expected a final reply after the deny round")
	}
}

// scriptedProvider replays a fixed sequence of ChatResponses so we can
// assert tool-loop behavior without hitting a real model.
type scriptedProvider struct {
	responses []*llm.ChatResponse
	calls     int
	lastReq   llm.ChatRequest
}

func (p *scriptedProvider) Name() string { return "scripted" }
func (p *scriptedProvider) Chat(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	p.lastReq = req
	if p.calls >= len(p.responses) {
		return &llm.ChatResponse{Content: "(out of script)"}, nil
	}
	resp := p.responses[p.calls]
	p.calls++
	return resp, nil
}
func (p *scriptedProvider) Stream(_ context.Context, _ llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	ch := make(chan llm.ChatEvent)
	close(ch)
	return ch, nil
}
func (p *scriptedProvider) ListModels(_ context.Context) ([]llm.ModelInfo, error) { return nil, nil }
func (p *scriptedProvider) ProbeModel(_ context.Context, _ string) (*llm.ModelInfo, error) {
	return nil, nil
}
func (p *scriptedProvider) LoadModel(_ context.Context, _ string, _ llm.LoadConfig) error {
	return nil
}

// stubFetchTool satisfies the web_fetch contract for the test without
// touching the network — the dispatcher only cares that something with
// Name() == "web_fetch" comes back from the registry.
type stubFetchTool struct{}

func (stubFetchTool) Name() string                                   { return "web_fetch" }
func (stubFetchTool) Description() string                            { return "stub fetch" }
func (stubFetchTool) Schema() json.RawMessage                        { return json.RawMessage(`{"type":"object"}`) }
func (stubFetchTool) Permission(tools.Context, json.RawMessage) tools.PermissionRequest {
	return tools.PermissionRequest{Decision: tools.PermissionAllow}
}
func (stubFetchTool) Run(_ tools.Context, _ json.RawMessage) (tools.Result, error) {
	return tools.Result{
		Title:   "fetched",
		Summary: "stubbed body",
		Content: []tools.ContentBlock{{Type: "text", Text: "Hello from stub"}},
	}, nil
}
