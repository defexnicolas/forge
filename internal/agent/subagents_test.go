package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"forge/internal/config"
	"forge/internal/gitops"
	"forge/internal/llm"
	"forge/internal/plans"
	"forge/internal/tools"
)

type batchFakeProvider struct {
	mu        sync.Mutex
	requests  []llm.ChatRequest
	loads     []string
	active    int
	maxActive int
	delay     time.Duration
}

func (f *batchFakeProvider) Name() string { return "fake" }
func (f *batchFakeProvider) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	f.mu.Lock()
	f.active++
	if f.active > f.maxActive {
		f.maxActive = f.active
	}
	f.requests = append(f.requests, req)
	f.mu.Unlock()
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	f.mu.Lock()
	f.active--
	f.mu.Unlock()
	content := "completed"
	if len(req.Messages) > 1 {
		msg := req.Messages[1].Content
		switch {
		case strings.Contains(msg, "alpha"):
			content = `{"status":"completed","summary":"alpha done"}`
		case strings.Contains(msg, "beta"):
			content = `{"status":"completed","summary":"beta done"}`
		case strings.Contains(msg, "small context"):
			content = `{"status":"completed","summary":"small context done"}`
		}
	}
	return &llm.ChatResponse{Content: content}, nil
}
func (f *batchFakeProvider) Stream(ctx context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
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
func (f *batchFakeProvider) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	return nil, nil
}
func (f *batchFakeProvider) ProbeModel(ctx context.Context, id string) (*llm.ModelInfo, error) {
	return nil, nil
}
func (f *batchFakeProvider) LoadModel(ctx context.Context, id string, cfg llm.LoadConfig) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loads = append(f.loads, id)
	return nil
}

type blockingProvider struct {
	requests []llm.ChatRequest
	name     string
}

func (p *blockingProvider) Name() string {
	if p.name != "" {
		return p.name
	}
	return "fake"
}
func (p *blockingProvider) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	p.requests = append(p.requests, req)
	<-ctx.Done()
	return nil, ctx.Err()
}
func (p *blockingProvider) Stream(ctx context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	p.requests = append(p.requests, req)
	<-ctx.Done()
	return nil, ctx.Err()
}
func (p *blockingProvider) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	return nil, nil
}
func (p *blockingProvider) ProbeModel(ctx context.Context, id string) (*llm.ModelInfo, error) {
	return nil, nil
}
func (p *blockingProvider) LoadModel(ctx context.Context, id string, cfg llm.LoadConfig) error {
	return nil
}

type streamOnlyProvider struct {
	requests []llm.ChatRequest
}

func (p *streamOnlyProvider) Name() string { return "fake" }
func (p *streamOnlyProvider) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	return nil, fmt.Errorf("Chat should not be used")
}
func (p *streamOnlyProvider) Stream(ctx context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	p.requests = append(p.requests, req)
	ch := make(chan llm.ChatEvent, 2)
	ch <- llm.ChatEvent{Type: "text", Text: `{"status":"completed","summary":"streamed ok"}`}
	ch <- llm.ChatEvent{Type: "done"}
	close(ch)
	return ch, nil
}
func (p *streamOnlyProvider) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	return nil, nil
}
func (p *streamOnlyProvider) ProbeModel(ctx context.Context, id string) (*llm.ModelInfo, error) {
	return nil, nil
}
func (p *streamOnlyProvider) LoadModel(ctx context.Context, id string, cfg llm.LoadConfig) error {
	return nil
}

// midStreamErrorProvider emits a valid tool_calls event followed by an error
// event — the same pattern LM Studio produces when the SSE connection drops
// after the model has already committed to a tool call. The Builder must
// process the partial progress instead of discarding it.
type midStreamErrorProvider struct {
	requests []llm.ChatRequest
}

func (p *midStreamErrorProvider) Name() string { return "fake" }
func (p *midStreamErrorProvider) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	return nil, fmt.Errorf("Chat should not be used")
}
func (p *midStreamErrorProvider) Stream(ctx context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	p.requests = append(p.requests, req)
	ch := make(chan llm.ChatEvent, 3)
	if len(p.requests) == 1 {
		ch <- llm.ChatEvent{Type: "tool_calls", ToolCalls: []llm.ToolCall{{
			ID:   "tc-1",
			Type: "function",
			Function: llm.FunctionCall{
				Name:      "task_update",
				Arguments: `{"id":"plan-1","status":"completed","summary":"done via partial stream"}`,
			},
		}}}
		ch <- llm.ChatEvent{Type: "error", Error: context.DeadlineExceeded}
	} else {
		ch <- llm.ChatEvent{Type: "text", Text: `{"status":"completed","summary":"final result"}`}
		ch <- llm.ChatEvent{Type: "done"}
	}
	close(ch)
	return ch, nil
}
func (p *midStreamErrorProvider) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	return nil, nil
}
func (p *midStreamErrorProvider) ProbeModel(ctx context.Context, id string) (*llm.ModelInfo, error) {
	return nil, nil
}
func (p *midStreamErrorProvider) LoadModel(ctx context.Context, id string, cfg llm.LoadConfig) error {
	return nil
}

func TestStreamSubagentResponsePreservesToolCallOnMidStreamError(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	providers := llm.NewRegistry()
	provider := &midStreamErrorProvider{}
	providers.Register(provider)
	runtime := newTestRuntime(t, cwd, cfg, registry, providers)

	tasksList, err := runtime.Tasks.ReplacePlan([]string{"plan-1 task"})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]any{"task_id": tasksList[0].ID})
	result, observation := runtime.executeExecuteTask(context.Background(), input)
	if result == nil {
		t.Fatal("expected execute_task result")
	}
	// The first stream errored mid-flight after a valid tool_call; the second
	// completes normally. Without the resilience patch the very first error
	// would propagate as a subagent failure (no "builder completed task").
	if !strings.Contains(observation, "builder completed task") {
		t.Fatalf("expected builder to recover from mid-stream error, got: %s", observation)
	}
	if len(provider.requests) < 2 {
		t.Fatalf("expected Builder to follow up with a second request after the partial tool call, got %d", len(provider.requests))
	}
}

func TestBuilderSubagentExists(t *testing.T) {
	registry := DefaultSubagents()
	builder, ok := registry.Get("builder")
	if !ok {
		t.Fatal("builder subagent should be registered")
	}
	if builder.ModelRole != "editor" {
		t.Fatalf("builder.ModelRole = %q, want editor", builder.ModelRole)
	}
	wantTools := []string{"read_file", "edit_file", "write_file", "apply_patch", "run_command", "task_get", "task_update"}
	for _, want := range wantTools {
		found := false
		for _, got := range builder.AllowedTools {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("builder.AllowedTools missing %q: %v", want, builder.AllowedTools)
		}
	}
}

func TestRunSubagentUsesStreamWhenAvailable(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	providers := llm.NewRegistry()
	provider := &streamOnlyProvider{}
	providers.Register(provider)
	runtime := newTestRuntime(t, cwd, cfg, registry, providers)

	result, err := runtime.RunSubagent(context.Background(), SubagentRequest{
		Agent:  "builder",
		Prompt: "small context",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Summary, "streamed ok") {
		t.Fatalf("expected streamed summary, got %#v", result)
	}
	if len(provider.requests) == 0 {
		t.Fatal("expected Stream to be used")
	}
}

// TestExecuteTaskPassesPlanDigestToBuilder verifies that the builder
// subagent receives the compactPlanDigest of the approved plan as part of
// its context. This is intentional: when one task hits a blocker that an
// adjacent task in the same plan would solve (e.g. "Task 2 sets up Docker"
// while Task 1 is choking on a host runtime version), the builder needs to
// see the surrounding plan to pull the adjacent step forward instead of
// returning task_too_large. The digest is truncated by compactPlanDigest
// (~600 chars total) so it stays cheap.
func TestExecuteTaskPassesPlanDigestToBuilder(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	provider := &batchFakeProvider{}
	providers := llm.NewRegistry()
	providers.Register(provider)
	runtime := newTestRuntime(t, cwd, cfg, registry, providers)
	runtime.SetGitSessionState(gitops.SessionState{
		RepoInitialized: true,
		DirtyWorktree:   true,
		BaselinePresent: true,
	})

	if _, err := runtime.Plans.Save(plans.Document{
		Summary:  "PLAN_SUMMARY_VISIBLE_TO_BUILDER",
		Approach: "PLAN_APPROACH_VISIBLE_TO_BUILDER",
	}); err != nil {
		t.Fatal(err)
	}
	tasksList, err := runtime.Tasks.ReplacePlan([]string{"BUILDER_TASK_TITLE"})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]any{
		"task_id":        tasksList[0].ID,
		"relevant_files": []string{"a.go", "b.go"},
	})
	result, _ := runtime.executeExecuteTask(context.Background(), input)
	if result == nil {
		t.Fatal("expected execute_task result")
	}
	if len(provider.requests) == 0 {
		t.Fatal("expected subagent to have invoked the provider")
	}
	// applySamplingDefaults pins the configured sampling on every request
	// (including subagents) — see runtime_guardrails.go. Builder no longer
	// "omits" temperature; verify it carries the configured default.
	got := provider.requests[0].Temperature
	if got == nil {
		t.Fatal("expected builder request to carry the configured temperature default")
	}
	if want := runtime.Config.Sampling.Temperature; *got != want {
		t.Fatalf("builder temperature = %v, want %v (configured default)", *got, want)
	}
	userMsg := provider.requests[0].Messages[1].Content
	if !strings.Contains(userMsg, "BUILDER_TASK_TITLE") {
		t.Fatalf("expected task title in builder prompt, got:\n%s", userMsg)
	}
	if !strings.Contains(userMsg, "Approved plan:") {
		t.Fatalf("expected 'Approved plan:' digest line in builder prompt, got:\n%s", userMsg)
	}
	if !strings.Contains(userMsg, "PLAN_SUMMARY_VISIBLE_TO_BUILDER") {
		t.Fatalf("expected plan summary in builder digest, got:\n%s", userMsg)
	}
	if !strings.Contains(userMsg, "PLAN_APPROACH_VISIBLE_TO_BUILDER") {
		t.Fatalf("expected plan approach in builder digest, got:\n%s", userMsg)
	}
	if !strings.Contains(userMsg, "a.go") || !strings.Contains(userMsg, "b.go") {
		t.Fatalf("expected relevant_files in builder context, got:\n%s", userMsg)
	}
	if !strings.Contains(userMsg, "Git state: Git: initialized, dirty worktree. Baseline: present.") {
		t.Fatalf("expected git state in builder context, got:\n%s", userMsg)
	}
}

func TestExecuteTaskRejectsMissingTaskID(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	providers := llm.NewRegistry()
	providers.Register(&batchFakeProvider{})
	runtime := newTestRuntime(t, cwd, cfg, registry, providers)

	input, _ := json.Marshal(map[string]string{"task_id": ""})
	result, observation := runtime.executeExecuteTask(context.Background(), input)
	if result == nil {
		t.Fatal("expected error result")
	}
	if !strings.Contains(observation, "task_id is required") {
		t.Fatalf("expected task_id required error, got %s", observation)
	}
}

func TestExecuteTaskTimeoutReturnsStructuredFailure(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "lmstudio"
	cfg.Providers.LMStudio.BaseURL = "http://localhost:1234/v1"
	cfg.Runtime.RequestTimeoutSeconds = 1
	cfg.Runtime.TaskTimeoutSeconds = 1
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	providers := llm.NewRegistry()
	providers.Register(&blockingProvider{name: "lmstudio"})
	runtime := newTestRuntime(t, cwd, cfg, registry, providers)

	tasksList, err := runtime.Tasks.ReplacePlan([]string{"Ship the task"})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]any{"task_id": tasksList[0].ID})
	result, observation := runtime.executeExecuteTask(context.Background(), input)
	if result == nil {
		t.Fatal("expected execute_task result")
	}
	if !strings.Contains(observation, "builder failed task") {
		t.Fatalf("expected builder failure observation, got %s", observation)
	}
	meta, ok := parseExecuteTaskFailureMeta(result)
	if !ok {
		t.Fatalf("expected structured failure metadata, got %#v", result)
	}
	if meta.TaskID != tasksList[0].ID || meta.FailureKind != "timeout" || !meta.TimedOut {
		t.Fatalf("unexpected execute_task failure metadata: %#v", meta)
	}
	if meta.Provider != "lmstudio" || meta.BaseURL != "http://localhost:1234/v1" || meta.Model != "local-model" {
		t.Fatalf("expected provider details in failure metadata, got %#v", meta)
	}
	if meta.ModelRole == "" || meta.Cause == "" || meta.Summary == "" {
		t.Fatalf("expected model role/cause/summary in failure metadata, got %#v", meta)
	}
	if !strings.Contains(meta.Summary, "timeout while waiting for provider response") {
		t.Fatalf("expected timeout summary, got %q", meta.Summary)
	}
}

func TestBuilderBlockedFromParallelBatch(t *testing.T) {
	// Parallel subagent dispatch must reject the builder: it owns mutating
	// tools and they cannot safely fan out — approvals would serialize and
	// concurrent patches race.
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	providers := llm.NewRegistry()
	providers.Register(&batchFakeProvider{})
	runtime := newTestRuntime(t, cwd, cfg, registry, providers)

	result, err := runtime.RunSubagents(context.Background(), SubagentBatchRequest{
		MaxConcurrency: 2,
		Tasks: []SubagentRequest{
			{Agent: "builder", Prompt: "edit a file"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content[0].Text, "parallel subagents do not allow mutating tools") {
		t.Fatalf("expected builder to be rejected in batch, got:\n%s", result.Content[0].Text)
	}
}

func TestRuntimeSpawnSubagentTool(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	providers := llm.NewRegistry()
	providers.Register(&fakeProvider{responses: []string{
		`{"status":"completed","summary":"found tools","findings":[],"changed_files":[],"suggested_next_steps":[]}`,
	}})
	runtime := newTestRuntime(t, cwd, cfg, registry, providers)

	input, _ := json.Marshal(map[string]string{"agent": "explorer", "prompt": "find tools"})
	result, observation := runtime.executeSubagent(context.Background(), input)
	if result == nil {
		t.Fatal("expected subagent result")
	}
	if !strings.Contains(observation, "found tools") {
		t.Fatalf("expected subagent observation, got %s", observation)
	}
}

func TestRunSubagentUsesTaskContextBudget(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	cfg.Context.Task.BudgetTokens = 1234
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	provider := &batchFakeProvider{}
	providers := llm.NewRegistry()
	providers.Register(provider)
	runtime := newTestRuntime(t, cwd, cfg, registry, providers)

	if _, err := runtime.RunSubagent(context.Background(), SubagentRequest{Agent: "explorer", Prompt: "small context"}); err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) == 0 {
		t.Fatal("expected provider request")
	}
	prompt := provider.requests[0].Messages[1].Content
	if !strings.Contains(prompt, "/1234") {
		t.Fatalf("expected task context budget in prompt, got:\n%s", prompt)
	}
}

func TestRunSubagentUsesProvidedSharedContext(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	provider := &batchFakeProvider{}
	providers := llm.NewRegistry()
	providers.Register(provider)
	runtime := newTestRuntime(t, cwd, cfg, registry, providers)

	shared, _ := json.Marshal(map[string]string{"text": "shared compact facts"})
	if _, err := runtime.RunSubagent(context.Background(), SubagentRequest{Agent: "explorer", Prompt: "small context", Context: shared}); err != nil {
		t.Fatal(err)
	}
	prompt := provider.requests[0].Messages[1].Content
	if !strings.Contains(prompt, "shared compact facts") {
		t.Fatalf("expected shared context in prompt, got:\n%s", prompt)
	}
}

func TestBuilderSubagentGetsHigherStepBudget(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	cfg.Runtime.MaxBuilderReadLoops = 8
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	if err := os.WriteFile(filepath.Join(cwd, "notes.txt"), []byte("builder context"), 0o644); err != nil {
		t.Fatal(err)
	}
	provider := &fakeProvider{responses: []string{
		`<tool_call>{"name":"read_file","input":{"path":"notes.txt"}}</tool_call>`,
		`<tool_call>{"name":"read_file","input":{"path":"notes.txt"}}</tool_call>`,
		`<tool_call>{"name":"read_file","input":{"path":"notes.txt"}}</tool_call>`,
		`<tool_call>{"name":"read_file","input":{"path":"notes.txt"}}</tool_call>`,
		`<tool_call>{"name":"read_file","input":{"path":"notes.txt"}}</tool_call>`,
		`{"status":"completed","summary":"finished after multiple steps"}`,
	}}
	providers := llm.NewRegistry()
	providers.Register(provider)
	runtime := newTestRuntime(t, cwd, cfg, registry, providers)

	result, err := runtime.RunSubagent(context.Background(), SubagentRequest{Agent: "builder", Prompt: "finish the task"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Summary, "finished after multiple steps") {
		t.Fatalf("expected builder to survive more than 4 steps, got %#v", result)
	}
	if len(provider.requests) == 0 {
		t.Fatal("expected provider request")
	}
	systemPrompt := provider.requests[0].Messages[0].Content
	if !strings.Contains(systemPrompt, "You MAY read files, edit files, apply patches, run allowed verification commands, and update task state.") {
		t.Fatalf("expected builder-specific prompt, got:\n%s", systemPrompt)
	}
}

func TestRunSubagentsPreservesOrderAndPartialErrors(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	provider := &batchFakeProvider{}
	providers := llm.NewRegistry()
	providers.Register(provider)
	runtime := newTestRuntime(t, cwd, cfg, registry, providers)

	result, err := runtime.RunSubagents(context.Background(), SubagentBatchRequest{
		MaxConcurrency: 2,
		Tasks: []SubagentRequest{
			{Agent: "explorer", Prompt: "alpha"},
			{Agent: "docs", Prompt: "should be rejected"},
			{Agent: "reviewer", Prompt: "beta"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Summary, "3 subagent task(s): 2 completed, 1 failed") {
		t.Fatalf("unexpected summary: %q", result.Summary)
	}
	text := result.Content[0].Text
	first := strings.Index(text, "[0] explorer completed")
	second := strings.Index(text, "[1] docs error")
	third := strings.Index(text, "[2] reviewer completed")
	if first < 0 || second < 0 || third < 0 || !(first < second && second < third) {
		t.Fatalf("expected ordered batch output, got:\n%s", text)
	}
}

func TestRunSubagentsConcurrentUnderSingleStrategy(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	cfg.ModelLoading.Enabled = true
	cfg.ModelLoading.Strategy = "single"
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	provider := &batchFakeProvider{delay: 50 * time.Millisecond}
	providers := llm.NewRegistry()
	providers.Register(provider)
	runtime := newTestRuntime(t, cwd, cfg, registry, providers)

	_, err := runtime.RunSubagents(context.Background(), SubagentBatchRequest{
		MaxConcurrency: 2,
		Tasks: []SubagentRequest{
			{Agent: "explorer", Prompt: "alpha"},
			{Agent: "reviewer", Prompt: "beta"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if provider.maxActive < 2 {
		t.Fatalf("expected concurrent requests under single strategy, maxActive=%d", provider.maxActive)
	}
}

func TestRuntimeTodoWriteUpdatesPlan(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	runtime := newTestRuntime(t, cwd, cfg, registry, llm.NewRegistry())

	input, _ := json.Marshal(map[string][]string{"items": {"read code in src/main.go", "write tests in src/main_test.go"}})
	result, _ := runtime.executeTodoWrite(input)
	if result == nil || !strings.HasPrefix(result.Summary, "Updated checklist:") {
		t.Fatalf("unexpected result %#v", result)
	}
	list, err := runtime.Tasks.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Title != "read code in src/main.go" {
		t.Fatalf("unexpected plan %#v", list)
	}
}

func TestRuntimeTaskTools(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	runtime := newTestRuntime(t, cwd, cfg, tools.NewRegistry(), llm.NewRegistry())

	input, _ := json.Marshal(map[string]string{"title": "ship sprint in cmd/forge/main.go", "notes": "subagents"})
	result, err := runtime.runTaskTool("task_create", input)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content[0].Text, "ship sprint") {
		t.Fatalf("expected task content, got %#v", result)
	}
	update, _ := json.Marshal(map[string]string{"id": "task-1", "status": "done"})
	result, err = runtime.runTaskTool("task_update", update)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content[0].Text, "completed") {
		t.Fatalf("expected completed task, got %#v", result)
	}
	update, _ = json.Marshal(map[string]string{"title": "ship sprint", "status": "in_progress"})
	result, err = runtime.runTaskTool("task_update", update)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content[0].Text, "in_progress") {
		t.Fatalf("expected title-based task update, got %#v", result)
	}
}

func TestRuntimePlanTools(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	runtime := newTestRuntime(t, cwd, cfg, tools.NewRegistry(), llm.NewRegistry())

	input := json.RawMessage(`{"summary":"split plan and todos","context":"panel is compact","approach":"save a detailed plan first","stubs":["plan_write","plan_get"],"validation":["go test ./..."]}`)
	result, err := runtime.runPlanTool("plan_write", input)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Summary, "split plan and todos") || !strings.Contains(result.Summary, "plan_write") {
		t.Fatalf("unexpected plan result %#v", result)
	}
	result, err = runtime.runPlanTool("plan_get", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content[0].Text, "Validation:") {
		t.Fatalf("expected full plan content, got %#v", result)
	}
}

func TestClaudeAliasesForSubagentsAndTasks(t *testing.T) {
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	for alias, want := range map[string]string{
		"Agent":      "spawn_subagent",
		"Task":       "spawn_subagent",
		"Agents":     "spawn_subagents",
		"Tasks":      "spawn_subagents",
		"PlanWrite":  "plan_write",
		"PlanGet":    "plan_get",
		"TaskCreate": "task_create",
		"TaskList":   "task_list",
		"TaskGet":    "task_get",
		"TaskUpdate": "task_update",
	} {
		tool, ok := registry.Get(alias)
		if !ok {
			t.Fatalf("expected alias %s", alias)
		}
		if tool.Name() != want {
			t.Fatalf("alias %s resolved to %s, want %s", alias, tool.Name(), want)
		}
	}
}
