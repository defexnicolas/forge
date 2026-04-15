package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"forge/internal/config"
	"forge/internal/llm"
	"forge/internal/tools"
)

type batchFakeProvider struct {
	mu       sync.Mutex
	requests []llm.ChatRequest
	loads    []string
}

func (f *batchFakeProvider) Name() string { return "fake" }
func (f *batchFakeProvider) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
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
	return nil, nil
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

func TestRuntimeTodoWriteUpdatesPlan(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	runtime := newTestRuntime(t, cwd, cfg, registry, llm.NewRegistry())

	input, _ := json.Marshal(map[string][]string{"items": {"read code", "write tests"}})
	result, _ := runtime.executeTodoWrite(input)
	if result == nil || !strings.HasPrefix(result.Summary, "Updated checklist:") {
		t.Fatalf("unexpected result %#v", result)
	}
	list, err := runtime.Tasks.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Title != "read code" {
		t.Fatalf("unexpected plan %#v", list)
	}
}

func TestRuntimeTaskTools(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	runtime := newTestRuntime(t, cwd, cfg, tools.NewRegistry(), llm.NewRegistry())

	input, _ := json.Marshal(map[string]string{"title": "ship sprint", "notes": "subagents"})
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
