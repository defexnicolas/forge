package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forge/internal/config"
	"forge/internal/llm"
	"forge/internal/tools"
)

// fakeProvider simulates a text-based (non-tool-calling) provider.
type fakeProvider struct {
	responses []string
	calls     int
}

func (f *fakeProvider) Name() string { return "fake" }
func (f *fakeProvider) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	if f.calls >= len(f.responses) {
		return &llm.ChatResponse{Content: "done"}, nil
	}
	content := f.responses[f.calls]
	f.calls++
	return &llm.ChatResponse{Content: content}, nil
}
func (f *fakeProvider) Stream(ctx context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
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
func (f *fakeProvider) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	return nil, nil
}

// fakeNativeProvider simulates a provider that supports native tool calling.
type fakeNativeProvider struct {
	steps []nativeStep
	calls int
}

type nativeStep struct {
	content   string
	toolCalls []llm.ToolCall
}

func (f *fakeNativeProvider) Name() string { return "fake" }
func (f *fakeNativeProvider) SupportsTools() bool { return true }
func (f *fakeNativeProvider) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	if f.calls >= len(f.steps) {
		return &llm.ChatResponse{Content: "done"}, nil
	}
	step := f.steps[f.calls]
	f.calls++
	return &llm.ChatResponse{Content: step.content, ToolCalls: step.toolCalls}, nil
}
func (f *fakeNativeProvider) Stream(ctx context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	resp, err := f.Chat(ctx, req)
	if err != nil {
		return nil, err
	}
	ch := make(chan llm.ChatEvent, 3)
	if resp.Content != "" {
		ch <- llm.ChatEvent{Type: "text", Text: resp.Content}
	}
	if len(resp.ToolCalls) > 0 {
		ch <- llm.ChatEvent{Type: "tool_calls", ToolCalls: resp.ToolCalls}
	}
	ch <- llm.ChatEvent{Type: "done"}
	close(ch)
	return ch, nil
}
func (f *fakeNativeProvider) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	return nil, nil
}

func TestRuntimeReadsFileThenAnswers(t *testing.T) {
	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "docs", "ARCHITECTURE.md"), []byte("Forge architecture"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	providers := llm.NewRegistry()
	providers.Register(&fakeProvider{responses: []string{
		`<tool_call>{"name":"read_file","input":{"path":"docs/ARCHITECTURE.md"}}</tool_call>`,
		`The document is about Forge architecture.`,
	}})

	runtime := NewRuntime(cwd, cfg, registry, providers)
	var texts []string
	for event := range runtime.Run(context.Background(), "resume @docs/ARCHITECTURE.md") {
		if event.Type == EventAssistantText || event.Type == EventAssistantDelta || event.Type == EventToolResult {
			texts = append(texts, event.Text)
		}
	}

	joined := strings.Join(texts, "\n")
	if !strings.Contains(joined, "docs/ARCHITECTURE.md") && !strings.Contains(joined, "Forge architecture") {
		t.Fatalf("expected tool result content, got:\n%s", joined)
	}
	if !strings.Contains(joined, "Forge architecture") {
		t.Fatalf("expected final answer, got:\n%s", joined)
	}
}

func TestRuntimeApprovesEditThenAnswers(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "file.txt"), []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	providers := llm.NewRegistry()
	providers.Register(&fakeProvider{responses: []string{
		`<tool_call>{"name":"edit_file","input":{"path":"file.txt","old_text":"world","new_text":"forge"}}</tool_call>`,
		`Edited the file.`,
	}})

	runtime := NewRuntime(cwd, cfg, registry, providers)
	var sawApproval bool
	var sawApplied bool
	for event := range runtime.Run(context.Background(), "edit a file") {
		if event.Type == EventApproval {
			sawApproval = true
			event.Approval.Response <- ApprovalResponse{Approved: true}
		}
		if event.Type == EventToolResult && strings.Contains(event.Text, "approved and applied") {
			sawApplied = true
		}
	}
	if !sawApproval || !sawApplied {
		t.Fatalf("expected approval and applied result, approval=%t applied=%t", sawApproval, sawApplied)
	}
	data, err := os.ReadFile(filepath.Join(cwd, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello forge\n" {
		t.Fatalf("unexpected edited content %q", data)
	}
	if _, err := runtime.UndoLast(); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(filepath.Join(cwd, "file.txt"))
	if string(data) != "hello world\n" {
		t.Fatalf("undo failed: %q", data)
	}
}

func TestRuntimeRejectsEditThenAnswers(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "file.txt"), []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	providers := llm.NewRegistry()
	providers.Register(&fakeProvider{responses: []string{
		`<tool_call>{"name":"edit_file","input":{"path":"file.txt","old_text":"world","new_text":"forge"}}</tool_call>`,
		`The edit was rejected.`,
	}})

	runtime := NewRuntime(cwd, cfg, registry, providers)
	var sawRejected bool
	for event := range runtime.Run(context.Background(), "edit a file") {
		if event.Type == EventApproval {
			event.Approval.Response <- ApprovalResponse{Approved: false}
		}
		if event.Type == EventToolResult && strings.Contains(event.Text, "rejected by user") {
			sawRejected = true
		}
	}
	if !sawRejected {
		t.Fatal("expected rejected tool result")
	}
	data, err := os.ReadFile(filepath.Join(cwd, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello world\n" {
		t.Fatalf("file should not change after rejection: %q", data)
	}
}

func TestRuntimeDeniesCommandTool(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	providers := llm.NewRegistry()
	providers.Register(&fakeProvider{responses: []string{
		`<tool_call>{"name":"run_command","input":{"command":"rm -rf ."}}</tool_call>`,
		`I cannot run commands.`,
	}})

	runtime := NewRuntime(cwd, cfg, registry, providers)
	var sawDeny bool
	for event := range runtime.Run(context.Background(), "run a command") {
		if event.Type == EventToolResult && strings.Contains(event.Text, "denied by command policy") {
			sawDeny = true
		}
	}
	if !sawDeny {
		t.Fatal("expected denied tool result")
	}
}

func TestRuntimeAllowsSafeCommandTool(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	providers := llm.NewRegistry()
	providers.Register(&fakeProvider{responses: []string{
		`<tool_call>{"name":"run_command","input":{"command":"git diff"}}</tool_call>`,
		`Diff checked.`,
	}})

	runtime := NewRuntime(cwd, cfg, registry, providers)
	var sawResult bool
	for event := range runtime.Run(context.Background(), "check diff") {
		if event.Type == EventToolResult && strings.Contains(event.Text, "git diff") {
			sawResult = true
		}
	}
	if !sawResult {
		t.Fatal("expected safe command result")
	}
}

func TestRuntimeApprovesAskCommandTool(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	providers := llm.NewRegistry()
	providers.Register(&fakeProvider{responses: []string{
		`<tool_call>{"name":"run_command","input":{"command":"python --version"}}</tool_call>`,
		`Command checked.`,
	}})

	runtime := NewRuntime(cwd, cfg, registry, providers)
	var sawApproval bool
	for event := range runtime.Run(context.Background(), "check python") {
		if event.Type == EventApproval {
			sawApproval = true
			event.Approval.Response <- ApprovalResponse{Approved: true}
		}
	}
	if !sawApproval {
		t.Fatal("expected command approval")
	}
}

func TestRuntimeNativeToolCallReadsFile(t *testing.T) {
	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "docs", "README.md"), []byte("Hello Forge"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	cfg.Providers.OpenAICompatible.SupportsTools = true
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	providers := llm.NewRegistry()
	providers.Register(&fakeNativeProvider{steps: []nativeStep{
		{
			content: "",
			toolCalls: []llm.ToolCall{
				{
					ID:   "call_1",
					Type: "function",
					Function: llm.FunctionCall{
						Name:      "read_file",
						Arguments: `{"path":"docs/README.md"}`,
					},
				},
			},
		},
		{content: "The README says Hello Forge."},
	}})

	runtime := NewRuntime(cwd, cfg, registry, providers)
	var sawToolResult bool
	var sawFinalText bool
	for event := range runtime.Run(context.Background(), "read the readme") {
		if event.Type == EventToolResult && strings.Contains(event.Text, "docs/README.md") {
			sawToolResult = true
		}
		if (event.Type == EventAssistantText || event.Type == EventAssistantDelta) && strings.Contains(event.Text, "Hello Forge") {
			sawFinalText = true
		}
	}
	if !sawToolResult {
		t.Fatal("expected tool result for read_file via native tool calling")
	}
	if !sawFinalText {
		t.Fatal("expected final text answer via native tool calling")
	}
}

func TestRuntimeNativeToolCallStreamingDeltas(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	providers := llm.NewRegistry()
	providers.Register(&fakeNativeProvider{steps: []nativeStep{
		{content: "I can help with that question."},
	}})

	runtime := NewRuntime(cwd, cfg, registry, providers)
	var deltas []string
	for event := range runtime.Run(context.Background(), "hello") {
		if event.Type == EventAssistantDelta {
			deltas = append(deltas, event.Text)
		}
	}
	joined := strings.Join(deltas, "")
	if !strings.Contains(joined, "help with that") {
		t.Fatalf("expected streaming deltas, got %q", joined)
	}
}

func TestRuntimeRejectsAskCommandTool(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	providers := llm.NewRegistry()
	providers.Register(&fakeProvider{responses: []string{
		`<tool_call>{"name":"run_command","input":{"command":"python --version"}}</tool_call>`,
		`Command rejected.`,
	}})

	runtime := NewRuntime(cwd, cfg, registry, providers)
	var sawRejected bool
	for event := range runtime.Run(context.Background(), "check python") {
		if event.Type == EventApproval {
			event.Approval.Response <- ApprovalResponse{Approved: false}
		}
		if event.Type == EventToolResult && strings.Contains(event.Text, "rejected by user") {
			sawRejected = true
		}
	}
	if !sawRejected {
		t.Fatal("expected rejected command result")
	}
}
