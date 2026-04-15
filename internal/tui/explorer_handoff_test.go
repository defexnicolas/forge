package tui

import (
	"context"
	"strings"
	"testing"

	"forge/internal/config"
	"forge/internal/llm"
	"forge/internal/session"
	"forge/internal/tools"

	tea "github.com/charmbracelet/bubbletea"
)

type tuiFakeProvider struct {
	responses []string
	requests  []llm.ChatRequest
	models    []llm.ModelInfo
	loads     []string
	calls     int
}

func (f *tuiFakeProvider) Name() string { return "fake" }
func (f *tuiFakeProvider) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	f.requests = append(f.requests, req)
	if f.calls >= len(f.responses) {
		return &llm.ChatResponse{Content: "done"}, nil
	}
	content := f.responses[f.calls]
	f.calls++
	return &llm.ChatResponse{Content: content}, nil
}
func (f *tuiFakeProvider) Stream(ctx context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	resp, err := f.Chat(ctx, req)
	if err != nil {
		return nil, err
	}
	ch := make(chan llm.ChatEvent, 2)
	if resp.Content != "" {
		ch <- llm.ChatEvent{Type: "text", Text: resp.Content}
	}
	ch <- llm.ChatEvent{Type: "done"}
	close(ch)
	return ch, nil
}
func (f *tuiFakeProvider) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	return f.models, nil
}
func (f *tuiFakeProvider) ProbeModel(ctx context.Context, modelID string) (*llm.ModelInfo, error) {
	for _, model := range f.models {
		if model.ID == modelID {
			info := model
			if info.LoadedContextLength <= 0 {
				info.LoadedContextLength = 16384
			}
			return &info, nil
		}
	}
	return nil, nil
}
func (f *tuiFakeProvider) LoadModel(ctx context.Context, modelID string, cfg llm.LoadConfig) error {
	f.loads = append(f.loads, modelID)
	return nil
}

func TestExplorerHandoffConfirmationStartsPlanMode(t *testing.T) {
	provider := &tuiFakeProvider{responses: []string{
		`{"status":"completed","summary":"found portal structure","findings":["Snake.js is missing"],"changed_files":[],"suggested_next_steps":["create plan"]}`,
		`<tool_call>{"name":"todo_write","input":{"items":["Create index.html","Implement Snake.js"]}}</tool_call>`,
		"Plan created.",
	}}
	m := newExplorerHandoffTestModel(t, provider)

	output := m.runSubagentCommand("explorer", "inspect game portal")
	if !strings.Contains(output, "found portal structure") {
		t.Fatalf("expected explorer output, got:\n%s", output)
	}
	if m.activeForm != formConfirmExplorerPlan {
		t.Fatalf("activeForm = %v, want formConfirmExplorerPlan", m.activeForm)
	}
	if !strings.Contains(m.pendingExplorerHandoff, "Snake.js is missing") {
		t.Fatalf("pending handoff missing finding: %q", m.pendingExplorerHandoff)
	}

	result, cmd, handled := m.handleFormUpdate(tea.KeyMsg{Type: tea.KeyEnter})
	if !handled {
		t.Fatal("expected confirm form to handle Enter")
	}
	updated := result.(*model)
	if updated.agentRuntime.Mode != "plan" {
		t.Fatalf("mode = %q, want plan", updated.agentRuntime.Mode)
	}
	if cmd == nil {
		t.Fatal("expected plan run command")
	}
	finalModel := drainAgentEvents(t, *updated, cmd)
	if finalModel.pendingExplorerHandoff != "" {
		t.Fatalf("expected TUI pending handoff cleared, got %q", finalModel.pendingExplorerHandoff)
	}
	if finalModel.agentRuntime.PendingExplorerContext != "" {
		t.Fatalf("expected runtime handoff consumed, got %q", finalModel.agentRuntime.PendingExplorerContext)
	}
	if len(provider.requests) < 2 {
		t.Fatalf("expected explorer and plan provider requests, got %d", len(provider.requests))
	}
	planPrompt := provider.requests[1].Messages[1].Content
	if !strings.Contains(planPrompt, "EXPLORER FINDINGS:") || !strings.Contains(planPrompt, "Snake.js is missing") {
		t.Fatalf("expected explorer findings in plan prompt, got:\n%s", planPrompt)
	}
}

func TestExplorerHandoffCanBeCanceled(t *testing.T) {
	provider := &tuiFakeProvider{responses: []string{
		`{"status":"completed","summary":"found facts","findings":["x"],"changed_files":[],"suggested_next_steps":[]}`,
	}}
	m := newExplorerHandoffTestModel(t, provider)
	_ = m.runSubagentCommand("explorer", "inspect")
	m.confirmExplorerPlan.selected = 1

	result, cmd, handled := m.handleFormUpdate(tea.KeyMsg{Type: tea.KeyEnter})
	if !handled {
		t.Fatal("expected confirm form to handle Enter")
	}
	updated := result.(*model)
	if cmd != nil {
		t.Fatal("expected no command when handoff is canceled")
	}
	if updated.agentRuntime.Mode != "build" {
		t.Fatalf("mode = %q, want build", updated.agentRuntime.Mode)
	}
	if updated.pendingExplorerHandoff != "" {
		t.Fatalf("expected pending handoff cleared, got %q", updated.pendingExplorerHandoff)
	}
}

func TestExploreModeCompletionCanHandoffToPlanMode(t *testing.T) {
	provider := &tuiFakeProvider{responses: []string{
		`Snake.js is missing and main.js contains the snake implementation.`,
		`<tool_call>{"name":"todo_write","input":{"items":["Move Snake code into snake.js","Import snake module from main.js"]}}</tool_call>`,
		"Plan created.",
	}}
	m := newExplorerHandoffTestModel(t, provider)
	if err := m.agentRuntime.SetMode("explore"); err != nil {
		t.Fatal(err)
	}
	m.agentEvents = m.agentRuntime.Run(context.Background(), "inspect snake files")
	m.agentRunning = true
	m = drainAgentEvents(t, m, waitForAgentEvent(m.agentEvents))

	if m.activeForm != formConfirmExplorerPlan {
		t.Fatalf("activeForm = %v, want formConfirmExplorerPlan", m.activeForm)
	}
	if !strings.Contains(m.pendingExplorerHandoff, "Snake.js is missing") {
		t.Fatalf("pending handoff missing explore output: %q", m.pendingExplorerHandoff)
	}

	result, cmd, handled := m.handleFormUpdate(tea.KeyMsg{Type: tea.KeyEnter})
	if !handled {
		t.Fatal("expected confirm form to handle Enter")
	}
	updated := result.(*model)
	if updated.agentRuntime.Mode != "plan" {
		t.Fatalf("mode = %q, want plan", updated.agentRuntime.Mode)
	}
	finalModel := drainAgentEvents(t, *updated, cmd)
	if len(provider.requests) < 2 {
		t.Fatalf("expected explore and plan provider requests, got %d", len(provider.requests))
	}
	planPrompt := provider.requests[1].Messages[1].Content
	if !strings.Contains(planPrompt, "EXPLORER FINDINGS:") || !strings.Contains(planPrompt, "main.js contains the snake implementation") {
		t.Fatalf("expected explore findings in plan prompt, got:\n%s", planPrompt)
	}
	if finalModel.pendingExplorerHandoff != "" {
		t.Fatalf("expected pending handoff cleared, got %q", finalModel.pendingExplorerHandoff)
	}
}

func TestPlanModeCompletionOffersBuildExecutionDefaultNo(t *testing.T) {
	provider := &tuiFakeProvider{responses: []string{
		`<tool_call>{"name":"todo_write","input":{"items":["Create snake.js","Update main.js"]}}</tool_call>`,
		"Plan created.",
	}}
	m := newExplorerHandoffTestModel(t, provider)
	if err := m.agentRuntime.SetMode("plan"); err != nil {
		t.Fatal(err)
	}
	m.agentEvents = m.agentRuntime.Run(context.Background(), "create plan")
	m.agentRunning = true
	m = drainAgentEvents(t, m, waitForAgentEvent(m.agentEvents))

	if m.activeForm != formConfirmExecute {
		t.Fatalf("activeForm = %v, want formConfirmExecute", m.activeForm)
	}
	if m.pendingExecuteLine == "" {
		t.Fatal("expected pending execute prompt")
	}
	if m.confirmExecute.selected != 1 {
		t.Fatalf("confirm selected = %d, want No by default", m.confirmExecute.selected)
	}

	result, cmd, handled := m.handleFormUpdate(tea.KeyMsg{Type: tea.KeyEnter})
	if !handled {
		t.Fatal("expected execute confirm form to handle Enter")
	}
	updated := result.(*model)
	if updated.agentRuntime.Mode != "plan" {
		t.Fatalf("mode = %q, want plan after default Enter", updated.agentRuntime.Mode)
	}
	if updated.agentRunning {
		t.Fatal("default Enter should not start build execution")
	}
	if cmd != nil {
		t.Fatal("default Enter should not return build command")
	}
}

func TestPlanModeCompletionExecutesWhenUserPressesY(t *testing.T) {
	provider := &tuiFakeProvider{responses: []string{
		`<tool_call>{"name":"todo_write","input":{"items":["Create snake.js","Update main.js"]}}</tool_call>`,
		"Plan created.",
		`{"status":"completed","summary":"explorer preflight"}`,
		`{"status":"completed","summary":"reviewer preflight"}`,
		"Executing plan.",
	}}
	m := newExplorerHandoffTestModel(t, provider)
	if err := m.agentRuntime.SetMode("plan"); err != nil {
		t.Fatal(err)
	}
	m.agentEvents = m.agentRuntime.Run(context.Background(), "create plan")
	m.agentRunning = true
	m = drainAgentEvents(t, m, waitForAgentEvent(m.agentEvents))

	result, cmd, handled := m.handleFormUpdate(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if !handled {
		t.Fatal("expected execute confirm form to handle y")
	}
	updated := result.(*model)
	if updated.agentRuntime.Mode != "build" {
		t.Fatalf("mode = %q, want build", updated.agentRuntime.Mode)
	}
	if !strings.Contains(updated.lastBuildPreflight, "explorer preflight") ||
		!strings.Contains(updated.lastBuildPreflight, "reviewer preflight") {
		t.Fatalf("expected preflight findings, got:\n%s", updated.lastBuildPreflight)
	}
	if cmd == nil {
		t.Fatal("expected build run command")
	}
	finalModel := drainAgentEvents(t, *updated, cmd)
	if len(provider.requests) < 4 {
		t.Fatalf("expected plan, 2 preflight, and build requests, got %d", len(provider.requests))
	}
	buildPrompt := provider.requests[len(provider.requests)-1].Messages[1].Content
	if !strings.Contains(buildPrompt, "BUILD PREFLIGHT FINDINGS:") ||
		!strings.Contains(buildPrompt, "explorer preflight") ||
		!strings.Contains(buildPrompt, "reviewer preflight") {
		t.Fatalf("expected build prompt to include preflight, got:\n%s", buildPrompt)
	}
	if finalModel.agentRuntime.PendingBuildPreflight != "" {
		t.Fatalf("expected preflight handoff consumed, got %q", finalModel.agentRuntime.PendingBuildPreflight)
	}
}

func newExplorerHandoffTestModel(t *testing.T, provider *tuiFakeProvider) model {
	t.Helper()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	// Preflight subagents are disabled by default (see config.Defaults); this
	// test specifically exercises the preflight path, so opt in explicitly.
	cfg.Build.Subagents.Enabled = true
	cwd := t.TempDir()
	store, err := session.New(cwd)
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	providers := llm.NewRegistry()
	providers.Register(provider)
	m := newModel(Options{CWD: cwd, Config: cfg, Tools: registry, Providers: providers, Session: store})
	t.Cleanup(func() {
		_ = m.agentRuntime.Close()
	})
	return m
}

func drainAgentEvents(t *testing.T, m model, cmd tea.Cmd) model {
	t.Helper()
	for cmd != nil {
		msg := cmd()
		updated, next := m.Update(msg)
		var ok bool
		m, ok = updated.(model)
		if !ok {
			ptr, ptrOK := updated.(*model)
			if !ptrOK {
				t.Fatalf("Update returned %T", updated)
			}
			m = *ptr
		}
		cmd = next
		if !m.agentRunning && m.agentEvents == nil {
			return m
		}
	}
	return m
}
