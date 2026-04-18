package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forge/internal/config"
	"forge/internal/llm"
	"forge/internal/plans"
	"forge/internal/tools"
)

// fakeProvider simulates a text-based (non-tool-calling) provider.
type fakeProvider struct {
	responses   []string
	requests    []llm.ChatRequest
	loads       []string
	loadConfigs []llm.LoadConfig
	calls       int
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
	steps []nativeStep
	calls int
}

type nativeStep struct {
	content   string
	toolCalls []llm.ToolCall
}

func (f *fakeNativeProvider) Name() string        { return "fake" }
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
func (f *fakeNativeProvider) ProbeModel(ctx context.Context, id string) (*llm.ModelInfo, error) {
	return nil, nil
}
func (f *fakeNativeProvider) LoadModel(ctx context.Context, id string, cfg llm.LoadConfig) error {
	return nil
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

func TestPlanPromptIncludesActiveChecklistWithExecuteTaskGuidance(t *testing.T) {
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

	for range runtime.Run(context.Background(), "continue") {
	}
	prompt := provider.requests[0].Messages[1].Content
	if !strings.Contains(prompt, "Plan document exists: active portal plan") ||
		!strings.Contains(prompt, "Active checklist: 1 pending, 0 in progress, 0 done") ||
		!strings.Contains(prompt, "execute_task") {
		t.Fatalf("expected active checklist guidance with execute_task, got:\n%s", prompt)
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
	if _, err := runtime.Plans.Save(plans.Document{Summary: "refactor plan"}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Tasks.ReplacePlan([]string{"Move code"}); err != nil {
		t.Fatal(err)
	}
	for range runtime.Run(context.Background(), "refine it") {
	}
	prompt := provider.requests[0].Messages[1].Content
	if !strings.Contains(prompt, "Plan document exists: refactor plan") ||
		!strings.Contains(prompt, "Active checklist: 1 pending, 0 in progress, 0 done") {
		t.Fatalf("expected existing plan in plan prompt, got:\n%s", prompt)
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
		if event.Type == EventAssistantText && strings.Contains(event.Text, "Plan created and saved") {
			sawSummary = true
		}
	}
	if !sawSummary {
		t.Fatal("expected native todo_write to emit local summary")
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
	if provider.loads[0] != "plan-model" {
		t.Fatalf("plan load = %#v, want plan-model first", provider.loads)
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
		if event.Progress.Phase == "waiting" {
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
