package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"forge/internal/config"
	"forge/internal/llm"
	"forge/internal/plans"
	"forge/internal/tools"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
}

// fakeProvider simulates a text-based (non-tool-calling) provider.
type fakeProvider struct {
	responses   []string
	requests    []llm.ChatRequest
	loads       []string
	loadConfigs []llm.LoadConfig
	calls       int
}

type fakeHistorySource struct {
	text string
}

func (f fakeHistorySource) ContextText(limit int) string {
	return f.text
}

func (f *fakeProvider) Name() string { return "fake" }
func (f *fakeProvider) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	f.requests = append(f.requests, req)
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
func (f *fakeProvider) ProbeModel(ctx context.Context, id string) (*llm.ModelInfo, error) {
	return nil, nil
}
func (f *fakeProvider) LoadModel(ctx context.Context, id string, cfg llm.LoadConfig) error {
	f.loads = append(f.loads, id)
	f.loadConfigs = append(f.loadConfigs, cfg)
	return nil
}

// fakeNativeProvider simulates a provider that supports native tool calling.
type fakeNativeProvider struct {
	steps    []nativeStep
	calls    int
	requests []llm.ChatRequest
}

type nativeStep struct {
	content   string
	toolCalls []llm.ToolCall
}

func (f *fakeNativeProvider) Name() string        { return "fake" }
func (f *fakeNativeProvider) SupportsTools() bool { return true }
func (f *fakeNativeProvider) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	f.requests = append(f.requests, req)
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
func (f *fakeNativeProvider) ProbeModel(ctx context.Context, id string) (*llm.ModelInfo, error) {
	return nil, nil
}
func (f *fakeNativeProvider) LoadModel(ctx context.Context, id string, cfg llm.LoadConfig) error {
	return nil
}

type fakeNativeFallbackProvider struct {
	requests []llm.ChatRequest
}

func (f *fakeNativeFallbackProvider) Name() string        { return "fake" }
func (f *fakeNativeFallbackProvider) SupportsTools() bool { return true }
func (f *fakeNativeFallbackProvider) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	f.requests = append(f.requests, req)
	if len(req.Tools) > 0 {
		return nil, os.ErrInvalid
	}
	return &llm.ChatResponse{Content: "fallback ok"}, nil
}
func (f *fakeNativeFallbackProvider) Stream(ctx context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	f.requests = append(f.requests, req)
	if len(req.Tools) > 0 {
		return nil, &fakeToolUnsupportedError{msg: "tools unsupported by current model"}
	}
	ch := make(chan llm.ChatEvent, 2)
	ch <- llm.ChatEvent{Type: "text", Text: "fallback ok"}
	ch <- llm.ChatEvent{Type: "done"}
	close(ch)
	return ch, nil
}
func (f *fakeNativeFallbackProvider) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	return nil, nil
}
func (f *fakeNativeFallbackProvider) ProbeModel(ctx context.Context, id string) (*llm.ModelInfo, error) {
	return nil, nil
}
func (f *fakeNativeFallbackProvider) LoadModel(ctx context.Context, id string, cfg llm.LoadConfig) error {
	return nil
}

type fakeToolUnsupportedError struct{ msg string }

func (e *fakeToolUnsupportedError) Error() string { return e.msg }

type scriptedTimeoutProvider struct {
	steps    []scriptedTimeoutStep
	requests []llm.ChatRequest
	calls    int
	name     string
}

type scriptedTimeoutStep struct {
	content string
	block   bool
}

func (p *scriptedTimeoutProvider) Name() string {
	if p.name != "" {
		return p.name
	}
	return "fake"
}
func (p *scriptedTimeoutProvider) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	p.requests = append(p.requests, req)
	if p.calls >= len(p.steps) {
		return &llm.ChatResponse{Content: "done"}, nil
	}
	step := p.steps[p.calls]
	p.calls++
	if step.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return &llm.ChatResponse{Content: step.content}, nil
}
func (p *scriptedTimeoutProvider) Stream(ctx context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	resp, err := p.Chat(ctx, req)
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
func (p *scriptedTimeoutProvider) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	return nil, nil
}
func (p *scriptedTimeoutProvider) ProbeModel(ctx context.Context, id string) (*llm.ModelInfo, error) {
	return nil, nil
}
func (p *scriptedTimeoutProvider) LoadModel(ctx context.Context, id string, cfg llm.LoadConfig) error {
	return nil
}

func TestNewRuntimeAdoptsConfigMaxSteps(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Runtime.MaxSteps = 17
	r := newTestRuntime(t, cwd, cfg, tools.NewRegistry(), llm.NewRegistry())
	if r.MaxSteps != 17 {
		t.Errorf("MaxSteps = %d, want 17 (from cfg.Runtime.MaxSteps)", r.MaxSteps)
	}
}

func TestNewRuntimeFallsBackTo40WhenConfigZero(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Runtime.MaxSteps = 0
	r := newTestRuntime(t, cwd, cfg, tools.NewRegistry(), llm.NewRegistry())
	if r.MaxSteps != 40 {
		t.Errorf("MaxSteps = %d, want 40 (built-in fallback)", r.MaxSteps)
	}
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

	runtime := newTestRuntime(t, cwd, cfg, registry, providers)
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

func TestPlanPromptIncludesActiveChecklistAndBuildHandoff(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	provider := &fakeProvider{responses: []string{"done"}}
	providers := llm.NewRegistry()
	providers.Register(provider)

	runtime := newTestRuntime(t, cwd, cfg, registry, providers)
	if _, err := runtime.Plans.Save(plans.Document{Summary: "active portal plan"}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Tasks.ReplacePlan([]string{"Create portal"}); err != nil {
		t.Fatal(err)
	}

	// Use a neutral message so the plan->build auto-switch in run() does not
	// fire — we want to verify the plan-mode prompt content here.
	for range runtime.Run(context.Background(), "what tasks remain?") {
	}
	prompt := provider.requests[0].Messages[1].Content
	if !strings.Contains(prompt, "Plan document exists: active portal plan") ||
		!strings.Contains(prompt, "Active checklist: 1 pending, 0 in progress, 0 done") ||
		!strings.Contains(prompt, "/mode build") {
		t.Fatalf("expected active checklist guidance pointing at build mode, got:\n%s", prompt)
	}
	if strings.Contains(prompt, "execute_task") {
		t.Fatalf("plan-mode prompt should not mention execute_task anymore, got:\n%s", prompt)
	}
}

func TestPlanModeAutoSwitchesToBuildOnExecuteIntent(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	provider := &fakeProvider{responses: []string{"done"}}
	providers := llm.NewRegistry()
	providers.Register(provider)

	runtime := newTestRuntime(t, cwd, cfg, registry, providers)
	if _, err := runtime.Plans.Save(plans.Document{Summary: "approved plan"}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Tasks.ReplacePlan([]string{"Apply the patch"}); err != nil {
		t.Fatal(err)
	}

	var sawSwitch bool
	for event := range runtime.Run(context.Background(), "execute the plan") {
		if event.Type == EventAssistantText && strings.Contains(event.Text, "Auto-switched to build mode") {
			sawSwitch = true
		}
	}
	if !sawSwitch {
		t.Fatal("expected auto-switch to build mode when execution intent fires on an approved plan")
	}
	if runtime.Mode != "build" {
		t.Fatalf("expected mode to be build after auto-switch, got %s", runtime.Mode)
	}
	prompt := provider.requests[0].Messages[1].Content
	if !strings.Contains(prompt, "Use the digest below first; call plan_get only if that digest is insufficient.") {
		t.Fatalf("expected build prompt to prefer plan digest before plan_get, got:\n%s", prompt)
	}
}

func TestBuildPromptOmitsSessionTimeline(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	provider := &fakeProvider{responses: []string{"done"}}
	providers := llm.NewRegistry()
	providers.Register(provider)

	runtime := newTestRuntime(t, cwd, cfg, registry, providers)
	runtime.Builder.History = fakeHistorySource{text: "Session summary:\nfirst request\n\nRecent timeline:\nfirst answer"}
	if _, err := runtime.Plans.Save(plans.Document{Summary: "approved plan"}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Tasks.ReplacePlan([]string{"Apply the patch"}); err != nil {
		t.Fatal(err)
	}

	for range runtime.Run(context.Background(), "execute the plan") {
	}
	prompt := provider.requests[0].Messages[1].Content
	if strings.Contains(prompt, "Session summary:") || strings.Contains(prompt, "Recent timeline:") {
		t.Fatalf("build prompt should omit session timeline, got:\n%s", prompt)
	}
}

func TestBuildModeRepromptsProseOnlyWhileTasksRemain(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	provider := &fakeProvider{responses: []string{
		"What's already in place:\n- state exists\n\nWhat's missing:\n- wire handlers",
		`<tool_call>{"name":"task_update","input":{"id":"plan-1","status":"completed","notes":"wired handlers"}}</tool_call>`,
	}}
	providers := llm.NewRegistry()
	providers.Register(provider)

	runtime := newTestRuntime(t, cwd, cfg, registry, providers)
	if _, err := runtime.Tasks.ReplacePlan([]string{"Wire handlers"}); err != nil {
		t.Fatal(err)
	}
	if err := runtime.SetMode("build"); err != nil {
		t.Fatal(err)
	}
	var sawCompletion bool
	for event := range runtime.Run(context.Background(), "execute the plan") {
		if event.Type == EventAssistantText && strings.Contains(event.Text, "All checklist tasks complete.") {
			sawCompletion = true
		}
	}
	if len(provider.requests) < 2 {
		t.Fatalf("expected build mode to reprompt after prose-only response, got %d request(s)", len(provider.requests))
	}
	lastUser := provider.requests[1].Messages[len(provider.requests[1].Messages)-1].Content
	if !strings.Contains(lastUser, "You are in BUILD mode and checklist tasks still remain") {
		t.Fatalf("expected build reprompt asking for tool call, got:\n%s", lastUser)
	}
	if !sawCompletion {
		t.Fatal("expected build turn to complete after reprompted tool call")
	}
}

func TestPlanPromptIncludesExistingPlanForRefinement(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	provider := &fakeProvider{responses: []string{"done"}}
	providers := llm.NewRegistry()
	providers.Register(provider)

	runtime := newTestRuntime(t, cwd, cfg, registry, providers)
	if _, err := runtime.Plans.Save(plans.Document{
		Summary:    "refactor plan",
		Approach:   "Update the rendering flow and keep the checklist incremental.",
		Validation: []string{"go test ./...", "manual smoke test"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Tasks.ReplacePlan([]string{"Move code"}); err != nil {
		t.Fatal(err)
	}
	for range runtime.Run(context.Background(), "refine it") {
	}
	prompt := provider.requests[0].Messages[1].Content
	if !strings.Contains(prompt, "Plan document exists: refactor plan") ||
		!strings.Contains(prompt, "Active checklist: 1 pending, 0 in progress, 0 done") ||
		!strings.Contains(prompt, "Plan digest:") ||
		!strings.Contains(prompt, "approach=Update the rendering flow and keep the checklist incremental.") ||
		!strings.Contains(prompt, "tasks=pending:Move code") {
		t.Fatalf("expected existing plan in plan prompt, got:\n%s", prompt)
	}
}

func TestPlanPromptExecutionIntentWithoutTasksStaysInPlan(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	provider := &fakeProvider{responses: []string{"done"}}
	providers := llm.NewRegistry()
	providers.Register(provider)

	runtime := newTestRuntime(t, cwd, cfg, registry, providers)
	if _, err := runtime.Plans.Save(plans.Document{Summary: "approved execution plan"}); err != nil {
		t.Fatal(err)
	}
	// No active tasks — auto-switch should NOT fire even with execute intent.

	for range runtime.Run(context.Background(), "execute the approved plan") {
	}
	if runtime.Mode != "plan" {
		t.Fatalf("expected to stay in plan mode without active tasks, got %s", runtime.Mode)
	}
}

func TestPlanRefinementPromptDoesNotAutoSwitchToBuild(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	provider := &fakeProvider{responses: []string{"done"}}
	providers := llm.NewRegistry()
	providers.Register(provider)

	runtime := newTestRuntime(t, cwd, cfg, registry, providers)
	if _, err := runtime.Plans.Save(plans.Document{Summary: "approved 2p plan"}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Tasks.ReplacePlan([]string{"Implement 2p mode"}); err != nil {
		t.Fatal(err)
	}

	line := "PLAN REFINEMENT REQUEST: continue with your plan to implement 2p in snake.\n\nRefine the existing plan and checklist for the user's latest request."
	for range runtime.Run(context.Background(), line) {
	}
	if runtime.Mode != "plan" {
		t.Fatalf("expected refinement prompt to stay in plan mode, got %s", runtime.Mode)
	}
	prompt := provider.requests[0].Messages[1].Content
	if !strings.Contains(prompt, "Default to refining the existing plan and checklist") {
		t.Fatalf("expected plan refinement guidance, got:\n%s", prompt)
	}
}

func TestExplorePromptUsesCompactContextAndSkipsPlanPointers(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	cfg.Context.BudgetTokens = 9000
	cfg.Context.Task.BudgetTokens = 1234
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	provider := &fakeProvider{responses: []string{"done"}}
	providers := llm.NewRegistry()
	providers.Register(provider)

	runtime := newTestRuntime(t, cwd, cfg, registry, providers)
	if _, err := runtime.Plans.Save(plans.Document{Summary: "stale menu plan"}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Tasks.ReplacePlan([]string{"Old task"}); err != nil {
		t.Fatal(err)
	}
	if err := runtime.SetMode("explore"); err != nil {
		t.Fatal(err)
	}

	for range runtime.Run(context.Background(), "analyze the project") {
	}
	if len(provider.requests) == 0 {
		t.Fatal("expected provider request")
	}
	prompt := provider.requests[0].Messages[1].Content
	if !strings.Contains(prompt, "Tokens: 0/1234") {
		t.Fatalf("expected compact explore token budget, got:\n%s", prompt)
	}
	if strings.Contains(prompt, "stale menu plan") || strings.Contains(prompt, "Old task") ||
		strings.Contains(prompt, "Plan document exists") || strings.Contains(prompt, "Checklist:") {
		t.Fatalf("explore prompt leaked plan/checklist state:\n%s", prompt)
	}
}

func TestRuntimeInjectsExplorerHandoffIntoPlanOnce(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	provider := &fakeProvider{responses: []string{"Plan noted."}}
	providers := llm.NewRegistry()
	providers.Register(provider)

	runtime := newTestRuntime(t, cwd, cfg, registry, providers)
	if err := runtime.SetMode("plan"); err != nil {
		t.Fatal(err)
	}
	runtime.PendingExplorerContext = "Explorer found Snake.js is missing and index.html should import script.js."
	for range runtime.Run(context.Background(), "create a plan from explorer findings") {
	}

	if runtime.PendingExplorerContext != "" {
		t.Fatalf("expected explorer handoff to be consumed, got %q", runtime.PendingExplorerContext)
	}
	if len(provider.requests) == 0 || len(provider.requests[0].Messages) < 2 {
		t.Fatalf("expected captured provider request, got %#v", provider.requests)
	}
	prompt := provider.requests[0].Messages[1].Content
	if !strings.Contains(prompt, "EXPLORER FINDINGS:") || !strings.Contains(prompt, "Snake.js is missing") {
		t.Fatalf("expected explorer findings in prompt, got:\n%s", prompt)
	}
}

// TestBuildPreflightDroppedForDeprecatedBuildMode asserts the legacy
// PendingBuildPreflight field is cleared on entry without leaking into the
// prompt — build mode no longer exists, the planner delegates to execute_task.
func TestBuildPreflightDroppedForDeprecatedBuildMode(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	provider := &fakeProvider{responses: []string{"Executing."}}
	providers := llm.NewRegistry()
	providers.Register(provider)

	runtime := newTestRuntime(t, cwd, cfg, registry, providers)
	runtime.PendingBuildPreflight = "stale explorer output"
	for range runtime.Run(context.Background(), "execute") {
	}
	if runtime.PendingBuildPreflight != "" {
		t.Fatalf("expected legacy preflight to be dropped, got %q", runtime.PendingBuildPreflight)
	}
	prompt := provider.requests[0].Messages[1].Content
	if strings.Contains(prompt, "BUILD PREFLIGHT FINDINGS:") || strings.Contains(prompt, "stale explorer output") {
		t.Fatalf("legacy build preflight leaked into prompt:\n%s", prompt)
	}
}

func TestPlanModeTextChecklistCreatesTodoFallback(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	provider := &fakeProvider{responses: []string{
		"Plan:\n1. Create snake.js module\n2. Update main.js imports\n3. Verify the game loads",
	}}
	providers := llm.NewRegistry()
	providers.Register(provider)

	runtime := newTestRuntime(t, cwd, cfg, registry, providers)
	if err := runtime.SetMode("plan"); err != nil {
		t.Fatal(err)
	}
	var sawTodoWrite bool
	var sawSummary bool
	for event := range runtime.Run(context.Background(), "plan snake refactor") {
		if event.Type == EventToolResult && event.ToolName == "todo_write" {
			sawTodoWrite = true
		}
		if event.Type == EventAssistantText && strings.Contains(event.Text, "Plan created and saved") {
			sawSummary = true
		}
	}
	if !sawTodoWrite {
		t.Fatal("expected text checklist fallback to create todo_write result")
	}
	if !sawSummary {
		t.Fatal("expected local plan summary")
	}
	list, err := runtime.Tasks.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 || list[0].Title != "Create snake.js module" {
		t.Fatalf("unexpected tasks: %#v", list)
	}
}

func TestPlanModePlainTextRepromptsThenCreatesTodo(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	provider := &fakeProvider{responses: []string{
		"I will think about the plan.",
		`<tool_call>{"name":"todo_write","input":{"items":["Create snake.js","Update main.js"]}}</tool_call>`,
		"Plan summary.",
	}}
	providers := llm.NewRegistry()
	providers.Register(provider)

	runtime := newTestRuntime(t, cwd, cfg, registry, providers)
	if err := runtime.SetMode("plan"); err != nil {
		t.Fatal(err)
	}
	for range runtime.Run(context.Background(), "plan snake refactor") {
	}
	if provider.calls < 2 {
		t.Fatalf("expected plan mode to reprompt prose-only response, calls=%d", provider.calls)
	}
	list, err := runtime.Tasks.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 tasks, got %#v", list)
	}
}

func TestPlanModeNativeTodoWriteEmitsLocalSummary(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	providers := llm.NewRegistry()
	providers.Register(&fakeNativeProvider{steps: []nativeStep{
		{
			toolCalls: []llm.ToolCall{
				{
					ID:   "call_1",
					Type: "function",
					Function: llm.FunctionCall{
						Name:      "todo_write",
						Arguments: `{"items":["Create snake.js","Update main.js"]}`,
					},
				},
			},
		},
	}})

	runtime := newTestRuntime(t, cwd, cfg, registry, providers)
	if err := runtime.SetMode("plan"); err != nil {
		t.Fatal(err)
	}
	var sawSummary bool
	for event := range runtime.Run(context.Background(), "plan snake refactor") {
		if event.Type == EventAssistantText && strings.Contains(event.Text, "Plan saved") && strings.Contains(event.Text, "/mode build") {
			sawSummary = true
		}
	}
	if !sawSummary {
		t.Fatal("expected native todo_write to emit plan-saved summary with build-mode handoff")
	}
}

func TestPlanModeNativePlanWriteThenTodoWrite(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	providers := llm.NewRegistry()
	providers.Register(&fakeNativeProvider{steps: []nativeStep{
		{
			toolCalls: []llm.ToolCall{
				{
					ID:   "call_1",
					Type: "function",
					Function: llm.FunctionCall{
						Name:      "plan_write",
						Arguments: `{"summary":"split plan from checklist","approach":"write detailed plan then todos","stubs":["plan_write tool"],"validation":["go test ./..."]}`,
					},
				},
				{
					ID:   "call_2",
					Type: "function",
					Function: llm.FunctionCall{
						Name:      "todo_write",
						Arguments: `{"items":["Add plan store","Wire plan tools"]}`,
					},
				},
			},
		},
	}})

	runtime := newTestRuntime(t, cwd, cfg, registry, providers)
	if err := runtime.SetMode("plan"); err != nil {
		t.Fatal(err)
	}
	var sawPlanWrite bool
	for event := range runtime.Run(context.Background(), "plan richer plans") {
		if event.Type == EventToolResult && event.ToolName == "plan_write" {
			sawPlanWrite = true
		}
	}
	if !sawPlanWrite {
		t.Fatal("expected plan_write tool result")
	}
	doc, ok, err := runtime.Plans.Current()
	if err != nil {
		t.Fatal(err)
	}
	if !ok || doc.Summary != "split plan from checklist" {
		t.Fatalf("unexpected plan %#v ok=%v", doc, ok)
	}
	list, err := runtime.Tasks.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Title != "Add plan store" {
		t.Fatalf("unexpected checklist %#v", list)
	}
}

func TestPlanModeExecuteTaskStopsAfterChecklistCompletes(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	provider := &fakeProvider{responses: []string{
		`<tool_call>{"name":"execute_task","input":{"task_id":"plan-1","relevant_files":["file.txt"]}}</tool_call>`,
		`<tool_call>{"name":"task_update","input":{"id":"plan-1","status":"completed"}}</tool_call>`,
		`{"status":"completed","summary":"builder finished"}`,
		`Execution complete.`,
	}}
	providers := llm.NewRegistry()
	providers.Register(provider)

	runtime := newTestRuntime(t, cwd, cfg, registry, providers)
	if err := runtime.SetMode("plan"); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Tasks.ReplacePlan([]string{"Ship the change"}); err != nil {
		t.Fatal(err)
	}

	var sawExecuteTask bool
	var sawFinalText bool
	for event := range runtime.Run(context.Background(), "execute the approved plan") {
		if event.Type == EventToolResult && event.ToolName == "execute_task" {
			sawExecuteTask = true
		}
		if (event.Type == EventAssistantText || event.Type == EventAssistantDelta) && strings.Contains(event.Text, "Execution complete.") {
			sawFinalText = true
		}
	}
	if !sawExecuteTask {
		t.Fatal("expected execute_task result")
	}
	if !sawFinalText {
		t.Fatal("expected final completion summary")
	}
	if provider.calls != 4 {
		t.Fatalf("expected planner->builder->builder->planner sequence with no replan, calls=%d", provider.calls)
	}
	list, err := runtime.Tasks.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Status != "completed" {
		t.Fatalf("expected checklist task to be completed, got %#v", list)
	}
}

func TestRuntimeUsesRoleModelsWhenModelMultiEnabled(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	cfg.ModelLoading.Enabled = true
	cfg.ModelLoading.Strategy = "single"
	cfg.Models["chat"] = "chat-model"
	cfg.Models["planner"] = "plan-model"
	cfg.Models["editor"] = "build-model"
	config.SetDetectedForRole(&cfg, "planner", &config.DetectedContext{ModelID: "plan-model", LoadedContextLength: 32000})
	config.SetDetectedForRole(&cfg, "editor", &config.DetectedContext{ModelID: "build-model", LoadedContextLength: 48000})

	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	provider := &fakeProvider{responses: []string{
		`<tool_call>{"name":"todo_write","input":{"items":["Plan task"]}}</tool_call>`,
		"Plan summary.",
	}}
	providers := llm.NewRegistry()
	providers.Register(provider)
	runtime := newTestRuntime(t, cwd, cfg, registry, providers)

	// Default mode is "plan" → uses the planner role model.
	for range runtime.Run(context.Background(), "plan it") {
	}
	if len(provider.requests) == 0 || provider.requests[0].Model != "plan-model" {
		t.Fatalf("plan request model = %#v, want plan-model", provider.requests)
	}
	if len(provider.loads) != 0 {
		t.Fatalf("expected detected planner model to skip load, got %#v", provider.loads)
	}
}

func TestRuntimeUsesEditorModelInBuildModeWhenModelMultiEnabled(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	cfg.ModelLoading.Enabled = true
	cfg.ModelLoading.Strategy = "single"
	cfg.Models["chat"] = "chat-model"
	cfg.Models["planner"] = "plan-model"
	cfg.Models["editor"] = "build-model"
	config.SetDetectedForRole(&cfg, "editor", &config.DetectedContext{ModelID: "build-model", LoadedContextLength: 48000})

	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	provider := &fakeProvider{responses: []string{"built"}}
	providers := llm.NewRegistry()
	providers.Register(provider)
	runtime := newTestRuntime(t, cwd, cfg, registry, providers)
	if err := runtime.SetMode("build"); err != nil {
		t.Fatal(err)
	}

	for range runtime.Run(context.Background(), "work the checklist") {
	}
	if len(provider.requests) == 0 || provider.requests[0].Model != "build-model" {
		t.Fatalf("build request model = %#v, want build-model", provider.requests)
	}
	if len(provider.loads) == 0 || provider.loads[0] != "build-model" {
		t.Fatalf("build load = %#v, want build-model first", provider.loads)
	}
}

func TestRuntimeUsesChatForMainModesUntilModelMultiEnabled(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	cfg.Models["chat"] = "chat-model"
	cfg.Models["planner"] = "plan-model"
	cfg.Models["editor"] = "build-model"

	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	provider := &fakeProvider{responses: []string{"planned"}}
	providers := llm.NewRegistry()
	providers.Register(provider)
	runtime := newTestRuntime(t, cwd, cfg, registry, providers)

	if err := runtime.SetMode("plan"); err != nil {
		t.Fatal(err)
	}
	for range runtime.Run(context.Background(), "plan it") {
	}
	if len(provider.requests) == 0 || provider.requests[0].Model != "chat-model" {
		t.Fatalf("request model = %#v, want chat-model", provider.requests)
	}
	// Without model-multi, per-role routing stays off and main modes keep
	// using the chat model. Forge still issues one startup load so
	// ParallelSlots reaches LM Studio (GEN slots wouldn't apply otherwise).
	if len(provider.loads) != 1 || provider.loads[0] != "chat-model" {
		t.Fatalf("expected exactly one startup slot-apply load on chat-model, got %#v", provider.loads)
	}
}

func TestRuntimeSkipsStartupReloadWhenDetectedModelAlreadyLoaded(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	cfg.Models["chat"] = "chat-model"
	cfg.Context.Detected = &config.DetectedContext{
		ModelID:             "chat-model",
		LoadedContextLength: 32000,
	}

	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	provider := &fakeProvider{responses: []string{"planned"}}
	providers := llm.NewRegistry()
	providers.Register(provider)
	runtime := newTestRuntime(t, cwd, cfg, registry, providers)

	if err := runtime.SetMode("plan"); err != nil {
		t.Fatal(err)
	}
	for range runtime.Run(context.Background(), "plan it") {
	}
	if len(provider.requests) == 0 || provider.requests[0].Model != "chat-model" {
		t.Fatalf("request model = %#v, want chat-model", provider.requests)
	}
	if len(provider.loads) != 0 {
		t.Fatalf("expected detected loaded model to skip startup reload, got %#v", provider.loads)
	}
}

func TestExplorerSubagentUsesExplorerModelRole(t *testing.T) {
	// Under strategy="parallel" we honor the subagent's declared role model
	// since LM Studio can keep multiple models resident at once.
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	cfg.ModelLoading.Enabled = true
	cfg.ModelLoading.Strategy = "parallel"
	cfg.Models["explorer"] = "explore-model"
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	provider := &fakeProvider{responses: []string{`{"status":"completed","summary":"ok"}`}}
	providers := llm.NewRegistry()
	providers.Register(provider)
	runtime := newTestRuntime(t, cwd, cfg, registry, providers)

	if _, err := runtime.RunSubagent(context.Background(), SubagentRequest{Agent: "explorer", Prompt: "inspect"}); err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) == 0 || provider.requests[0].Model != "explore-model" {
		t.Fatalf("explorer model = %#v, want explore-model", provider.requests)
	}
}

// Under strategy="single" a subagent must NOT swap models; it runs on the
// model loaded for the current mode so LM Studio doesn't thrash. The default
// mode is now "plan" which resolves to the "planner" role.
func TestSingleStrategySubagentReusesCurrentModel(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	cfg.ModelLoading.Enabled = true
	cfg.ModelLoading.Strategy = "single"
	cfg.Models["planner"] = "planner-model"
	cfg.Models["explorer"] = "explore-model"
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	provider := &fakeProvider{responses: []string{`{"status":"completed","summary":"ok"}`}}
	providers := llm.NewRegistry()
	providers.Register(provider)
	runtime := newTestRuntime(t, cwd, cfg, registry, providers)

	if _, err := runtime.RunSubagent(context.Background(), SubagentRequest{Agent: "explorer", Prompt: "inspect"}); err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) == 0 || provider.requests[0].Model != "planner-model" {
		t.Fatalf("under single strategy + plan mode, subagent model = %#v, want planner-model", provider.requests)
	}
}

func TestParallelModelLoadingSkipsMarkedLoadedModel(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	cfg.ModelLoading.Enabled = true
	cfg.ModelLoading.Strategy = "parallel"
	cfg.Models["explorer"] = "explore-model"
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	provider := &fakeProvider{responses: []string{`{"status":"completed","summary":"ok"}`}}
	providers := llm.NewRegistry()
	providers.Register(provider)
	runtime := newTestRuntime(t, cwd, cfg, registry, providers)
	runtime.MarkModelLoaded("explore-model")

	if _, err := runtime.RunSubagent(context.Background(), SubagentRequest{Agent: "explorer", Prompt: "inspect"}); err != nil {
		t.Fatal(err)
	}
	if len(provider.loads) != 0 {
		t.Fatalf("parallel strategy should not reload marked model, got %#v", provider.loads)
	}
}

func TestReloadCurrentModelAppliesParallelSlots(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	cfg.ModelLoading.Enabled = true
	cfg.ModelLoading.Strategy = "single"
	cfg.ModelLoading.ParallelSlots = 2
	cfg.Context.ModelContextTokens = 32000
	cfg.Models["planner"] = "planner-model"
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	provider := &fakeProvider{}
	providers := llm.NewRegistry()
	providers.Register(provider)
	runtime := newTestRuntime(t, cwd, cfg, registry, providers)

	modelID, err := runtime.ReloadCurrentModel(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if modelID != "planner-model" {
		t.Fatalf("modelID = %q, want planner-model", modelID)
	}
	if len(provider.loadConfigs) != 1 {
		t.Fatalf("expected one load config, got %#v", provider.loadConfigs)
	}
	got := provider.loadConfigs[0]
	if got.ParallelSlots != 2 || got.ContextLength != 32000 || !got.FlashAttention {
		t.Fatalf("unexpected load config: %#v", got)
	}
}

func TestRuntimeApprovesEditThenAnswers(t *testing.T) {
	requireGit(t)
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

	runtime := newTestRuntime(t, cwd, cfg, registry, providers)
	runtime.Policy = NewSprintPolicy()
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

func TestRuntimeApprovalBootstrapsGitRepo(t *testing.T) {
	requireGit(t)
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

	runtime := newTestRuntime(t, cwd, cfg, registry, providers)
	runtime.Policy = NewSprintPolicy()
	if !runtime.GitSessionState().AutoInitialized {
		t.Fatalf("expected runtime to auto-initialize git state at session start, got %#v", runtime.GitSessionState())
	}
	var approvalDiff string
	for event := range runtime.Run(context.Background(), "edit a file") {
		if event.Type == EventApproval {
			approvalDiff = event.Approval.Diff
			event.Approval.Response <- ApprovalResponse{Approved: true}
		}
	}
	if strings.Contains(approvalDiff, "initialize a git repository") {
		t.Fatalf("bootstrap should be surfaced before approval now, got approval diff:\n%s", approvalDiff)
	}
	if _, err := os.Stat(filepath.Join(cwd, ".git")); err != nil {
		t.Fatalf("expected .git directory after approved edit: %v", err)
	}
}

func TestRuntimeApprovalSnapshotsDirtyTree(t *testing.T) {
	requireGit(t)
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

	runtime := newTestRuntime(t, cwd, cfg, registry, providers)
	runtime.Policy = NewSprintPolicy()
	if _, err := exec.Command("git", "-C", cwd, "init").CombinedOutput(); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.Command("git", "-C", cwd, "config", "--local", "user.name", "Tester").CombinedOutput(); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.Command("git", "-C", cwd, "config", "--local", "user.email", "tester@example.com").CombinedOutput(); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.Command("git", "-C", cwd, "add", "-A").CombinedOutput(); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", cwd, "commit", "--allow-empty", "-m", "baseline").CombinedOutput(); err != nil {
		t.Fatalf("baseline commit failed: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(cwd, "other.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var approvalDiff string
	for event := range runtime.Run(context.Background(), "edit a file") {
		if event.Type == EventApproval {
			approvalDiff = event.Approval.Diff
			event.Approval.Response <- ApprovalResponse{Approved: true}
		}
	}
	if !strings.Contains(approvalDiff, "snapshot commit") {
		t.Fatalf("expected dirty-tree snapshot note in approval diff, got:\n%s", approvalDiff)
	}
	out, err := exec.Command("git", "-C", cwd, "log", "--oneline", "-2").CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), cfg.Git.SnapshotCommitMessage) {
		t.Fatalf("expected snapshot commit in recent history, got:\n%s", out)
	}
}

func TestRuntimeRejectsEditThenAnswers(t *testing.T) {
	requireGit(t)
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

	runtime := newTestRuntime(t, cwd, cfg, registry, providers)
	runtime.Policy = NewSprintPolicy()
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

	runtime := newTestRuntime(t, cwd, cfg, registry, providers)
	runtime.Policy = NewSprintPolicy()
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

	runtime := newTestRuntime(t, cwd, cfg, registry, providers)
	runtime.Policy = NewSprintPolicy()
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

	runtime := newTestRuntime(t, cwd, cfg, registry, providers)
	runtime.Policy = NewSprintPolicy()
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

	runtime := newTestRuntime(t, cwd, cfg, registry, providers)
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

func TestBuildModeNativeReadOnlyLoopStopsEarly(t *testing.T) {
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
	cfg.Runtime.MaxBuilderReadLoops = 8
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	providers := llm.NewRegistry()
	providers.Register(&fakeNativeProvider{steps: []nativeStep{
		{toolCalls: []llm.ToolCall{{ID: "call_1", Type: "function", Function: llm.FunctionCall{Name: "read_file", Arguments: `{"path":"docs/README.md"}`}}}},
		{toolCalls: []llm.ToolCall{{ID: "call_2", Type: "function", Function: llm.FunctionCall{Name: "read_file", Arguments: `{"path":"docs/README.md"}`}}}},
		{toolCalls: []llm.ToolCall{{ID: "call_3", Type: "function", Function: llm.FunctionCall{Name: "read_file", Arguments: `{"path":"docs/README.md"}`}}}},
		{toolCalls: []llm.ToolCall{{ID: "call_4", Type: "function", Function: llm.FunctionCall{Name: "read_file", Arguments: `{"path":"docs/README.md"}`}}}},
		{toolCalls: []llm.ToolCall{{ID: "call_5", Type: "function", Function: llm.FunctionCall{Name: "read_file", Arguments: `{"path":"docs/README.md"}`}}}},
	}})

	runtime := newTestRuntime(t, cwd, cfg, registry, providers)
	if err := runtime.SetMode("build"); err != nil {
		t.Fatal(err)
	}
	var sawErr error
	for event := range runtime.Run(context.Background(), "finish the task") {
		if event.Type == EventError {
			sawErr = event.Error
		}
	}
	if sawErr == nil {
		t.Fatal("expected build read-only loop error")
	}
	if !strings.Contains(sawErr.Error(), "repeated read_file on docs/README.md") {
		t.Fatalf("unexpected error: %v", sawErr)
	}
}

func TestBuildModeAllowsSixDistinctReadOnlyCalls(t *testing.T) {
	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 6; i++ {
		name := filepath.Join(cwd, "docs", fmt.Sprintf("file%d.txt", i))
		if err := os.WriteFile(name, []byte(fmt.Sprintf("content %d", i)), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	cfg.Providers.OpenAICompatible.SupportsTools = true
	cfg.Runtime.MaxBuilderReadLoops = 8
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	providers := llm.NewRegistry()
	providers.Register(&fakeNativeProvider{steps: []nativeStep{
		{toolCalls: []llm.ToolCall{{ID: "call_1", Type: "function", Function: llm.FunctionCall{Name: "read_file", Arguments: `{"path":"docs/file1.txt"}`}}}},
		{toolCalls: []llm.ToolCall{{ID: "call_2", Type: "function", Function: llm.FunctionCall{Name: "read_file", Arguments: `{"path":"docs/file2.txt"}`}}}},
		{toolCalls: []llm.ToolCall{{ID: "call_3", Type: "function", Function: llm.FunctionCall{Name: "read_file", Arguments: `{"path":"docs/file3.txt"}`}}}},
		{toolCalls: []llm.ToolCall{{ID: "call_4", Type: "function", Function: llm.FunctionCall{Name: "read_file", Arguments: `{"path":"docs/file4.txt"}`}}}},
		{toolCalls: []llm.ToolCall{{ID: "call_5", Type: "function", Function: llm.FunctionCall{Name: "read_file", Arguments: `{"path":"docs/file5.txt"}`}}}},
		{toolCalls: []llm.ToolCall{{ID: "call_6", Type: "function", Function: llm.FunctionCall{Name: "read_file", Arguments: `{"path":"docs/file6.txt"}`}}}},
		{content: "Ready to edit."},
	}})

	runtime := newTestRuntime(t, cwd, cfg, registry, providers)
	if err := runtime.SetMode("build"); err != nil {
		t.Fatal(err)
	}
	var sawErr error
	var sawFinalText bool
	for event := range runtime.Run(context.Background(), "finish the task") {
		if event.Type == EventError {
			sawErr = event.Error
		}
		if (event.Type == EventAssistantText || event.Type == EventAssistantDelta) && strings.Contains(event.Text, "Ready to edit.") {
			sawFinalText = true
		}
	}
	if sawErr != nil {
		t.Fatalf("did not expect build read-only guard for distinct files: %v", sawErr)
	}
	if !sawFinalText {
		t.Fatal("expected build turn to continue after six distinct reads")
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

	runtime := newTestRuntime(t, cwd, cfg, registry, providers)
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

func TestRuntimeFallsBackWhenNativeToolsUnsupported(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	providers := llm.NewRegistry()
	provider := &fakeNativeFallbackProvider{}
	providers.Register(provider)

	runtime := newTestRuntime(t, cwd, cfg, registry, providers)
	var sawFinalText bool
	for event := range runtime.Run(context.Background(), "hello") {
		if (event.Type == EventAssistantText || event.Type == EventAssistantDelta) && strings.Contains(event.Text, "fallback ok") {
			sawFinalText = true
		}
	}
	if !sawFinalText {
		t.Fatal("expected fallback text answer after tool-calling rejection")
	}
	if len(provider.requests) < 2 {
		t.Fatalf("expected retry without tools, got %d request(s)", len(provider.requests))
	}
	if len(provider.requests[0].Tools) == 0 {
		t.Fatal("expected first request to include native tools")
	}
	if len(provider.requests[len(provider.requests)-1].Tools) != 0 {
		t.Fatal("expected final retry to omit tools")
	}
}

func TestRuntimeEmitsModelProgressDuringStreaming(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	providers := llm.NewRegistry()
	providers.Register(&fakeProvider{responses: []string{"Forge is streaming progress."}})

	runtime := newTestRuntime(t, cwd, cfg, registry, providers)
	var sawWaiting bool
	var sawOutput bool
	for event := range runtime.Run(context.Background(), "hello") {
		if event.Type != EventModelProgress || event.Progress == nil {
			continue
		}
		if event.Progress.Phase == "waiting_on_provider" {
			sawWaiting = true
		}
		if event.Progress.OutputTokens > 0 && event.Progress.TotalTokens >= event.Progress.InputTokens {
			sawOutput = true
		}
	}
	if !sawWaiting {
		t.Fatal("expected a waiting progress event before streaming")
	}
	if !sawOutput {
		t.Fatal("expected progress event with output token estimate")
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

	runtime := newTestRuntime(t, cwd, cfg, registry, providers)
	runtime.Policy = NewSprintPolicy()
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
