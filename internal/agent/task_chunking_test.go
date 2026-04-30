package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"forge/internal/config"
	"forge/internal/llm"
	"forge/internal/tools"
)

func TestTodoWriteKeepsSmallHTMLArtifactSingle(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	runtime := newTestRuntime(t, cwd, cfg, tools.NewRegistry(), llm.NewRegistry())

	input, _ := json.Marshal(map[string]any{"items": []string{"Create index.html"}})
	if _, observation := runtime.executeTodoWrite(input); !strings.Contains(observation, "Todo plan") {
		t.Fatalf("expected todo_write observation, got %s", observation)
	}
	list, err := runtime.Tasks.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Title != "Create index.html" {
		t.Fatalf("unexpected tasks after one-shot todo_write: %#v", list)
	}
}

func TestTodoWriteExpandsLargeMarkdownTask(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	runtime := newTestRuntime(t, cwd, cfg, tools.NewRegistry(), llm.NewRegistry())

	input, _ := json.Marshal(map[string]any{"items": []string{"Write comprehensive README.md documentation"}})
	if _, observation := runtime.executeTodoWrite(input); !strings.Contains(observation, "Todo plan") {
		t.Fatalf("expected todo_write observation, got %s", observation)
	}
	list, err := runtime.Tasks.List()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"Create README.md outline",
		"Write opening sections in README.md",
		"Write remaining sections in README.md",
		"Polish examples and cleanup in README.md",
	}
	if len(list) != len(want) {
		t.Fatalf("expected %d expanded tasks, got %#v", len(want), list)
	}
	for i, task := range list {
		if task.Title != want[i] {
			t.Fatalf("task %d = %q, want %q", i, task.Title, want[i])
		}
	}
}

func TestTodoWriteKeepsSmallJSModuleTaskSingle(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	runtime := newTestRuntime(t, cwd, cfg, tools.NewRegistry(), llm.NewRegistry())

	input, _ := json.Marshal(map[string]any{"items": []string{"Create snake.js module"}})
	if _, observation := runtime.executeTodoWrite(input); !strings.Contains(observation, "Todo plan") {
		t.Fatalf("expected todo_write observation, got %s", observation)
	}
	list, err := runtime.Tasks.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Title != "Create snake.js module" {
		t.Fatalf("unexpected tasks after non-chunked todo_write: %#v", list)
	}
}

func TestExecuteTaskIncludesChunkingContextForScaffoldTask(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	provider := &batchFakeProvider{}
	providers := llm.NewRegistry()
	providers.Register(provider)
	runtime := newTestRuntime(t, cwd, cfg, registry, providers)

	tasksList, err := runtime.Tasks.ReplacePlan([]string{"Create index.html scaffold"})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]any{"task_id": tasksList[0].ID})
	if _, observation := runtime.executeExecuteTask(context.Background(), input); !strings.Contains(observation, "builder completed task") {
		t.Fatalf("expected execute_task observation, got %s", observation)
	}
	if len(provider.requests) == 0 {
		t.Fatal("expected builder request")
	}
	userMsg := provider.requests[0].Messages[1].Content
	for _, needle := range []string{"target file: index.html", "file strategy: scaffold_then_patch", "section goal: scaffold"} {
		if !strings.Contains(userMsg, needle) {
			t.Fatalf("expected chunking context %q in builder prompt, got:\n%s", needle, userMsg)
		}
	}
}

func TestExecuteTaskIncludesOneShotArtifactContextForSmallHTMLTask(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	provider := &batchFakeProvider{}
	providers := llm.NewRegistry()
	providers.Register(provider)
	runtime := newTestRuntime(t, cwd, cfg, registry, providers)

	tasksList, err := runtime.Tasks.ReplacePlan([]string{"Create index.html"})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]any{"task_id": tasksList[0].ID})
	if _, observation := runtime.executeExecuteTask(context.Background(), input); !strings.Contains(observation, "builder completed task") {
		t.Fatalf("expected execute_task observation, got %s", observation)
	}
	if len(provider.requests) == 0 {
		t.Fatal("expected builder request")
	}
	userMsg := provider.requests[0].Messages[1].Content
	for _, needle := range []string{"target file: index.html", "file strategy: one_shot_artifact"} {
		if !strings.Contains(userMsg, needle) {
			t.Fatalf("expected one-shot context %q in builder prompt, got:\n%s", needle, userMsg)
		}
	}
}

func TestPrepareMutationRejectsOneShotWriteForChunkedSectionTask(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	runtime := newTestRuntime(t, cwd, cfg, tools.NewRegistry(), llm.NewRegistry())
	runtime.setActiveBuilderTask(&builderTaskGuard{
		TaskID:       "plan-1",
		TargetFile:   "index.html",
		FileStrategy: "scaffold_then_patch",
		SectionGoal:  "head_metadata",
	})
	defer runtime.setActiveBuilderTask(nil)

	input, _ := json.Marshal(map[string]string{
		"path":    "index.html",
		"content": "<html><head><title>full page</title></head><body><main>all content at once</main></body></html>",
	})
	if _, _, err := runtime.prepareMutation("write_file", input); err == nil || !strings.Contains(err.Error(), "do not create the full file in one shot") {
		t.Fatalf("expected one-shot write rejection, got %v", err)
	}
}

func TestPrepareMutationAllowsSmallScaffoldWrite(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	runtime := newTestRuntime(t, cwd, cfg, tools.NewRegistry(), llm.NewRegistry())
	runtime.setActiveBuilderTask(&builderTaskGuard{
		TaskID:         "plan-1",
		TargetFile:     "index.html",
		FileStrategy:   "scaffold_then_patch",
		SectionGoal:    "scaffold",
		AllowFullWrite: true,
	})
	defer runtime.setActiveBuilderTask(nil)

	input, _ := json.Marshal(map[string]string{
		"path":    "index.html",
		"content": "<!doctype html>\n<html>\n<head>\n  <title></title>\n</head>\n<body>\n</body>\n</html>\n",
	})
	plan, summary, err := runtime.prepareMutation("write_file", input)
	if err != nil {
		t.Fatal(err)
	}
	if summary != "create index.html" || len(plan.Operations) == 0 {
		t.Fatalf("expected scaffold write plan, got summary=%q plan=%#v", summary, plan)
	}
}

func TestTodoWriteExpandsSnakeGameInHTML(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	runtime := newTestRuntime(t, cwd, cfg, tools.NewRegistry(), llm.NewRegistry())

	input, _ := json.Marshal(map[string]any{"items": []string{"create snake game in html"}})
	if _, observation := runtime.executeTodoWrite(input); !strings.Contains(observation, "Todo plan") {
		t.Fatalf("expected todo_write observation, got %s", observation)
	}
	list, err := runtime.Tasks.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) < 5 {
		t.Fatalf("expected snake-game.html to expand into >=5 tasks, got %d: %#v", len(list), list)
	}
	for _, task := range list {
		if !strings.Contains(task.Title, ".html") {
			t.Fatalf("expected every expanded task to reference an .html file, got %q", task.Title)
		}
	}
	if !strings.Contains(list[0].Title, "scaffold") {
		t.Fatalf("expected first expanded task to be the scaffold, got %q", list[0].Title)
	}
}

func TestTodoWriteExpandsTodoAppInReact(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	runtime := newTestRuntime(t, cwd, cfg, tools.NewRegistry(), llm.NewRegistry())

	input, _ := json.Marshal(map[string]any{"items": []string{"build a todo app in react"}})
	if _, observation := runtime.executeTodoWrite(input); !strings.Contains(observation, "Todo plan") {
		t.Fatalf("expected todo_write observation, got %s", observation)
	}
	list, err := runtime.Tasks.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) < 5 {
		t.Fatalf("expected todo-react task to expand into >=5 tasks, got %d: %#v", len(list), list)
	}
	for _, task := range list {
		if !strings.Contains(task.Title, "App.tsx") {
			t.Fatalf("expected every expanded task to reference App.tsx, got %q", task.Title)
		}
	}
}

func TestTodoWriteExpandsCalculatorAsWebpage(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	runtime := newTestRuntime(t, cwd, cfg, tools.NewRegistry(), llm.NewRegistry())

	input, _ := json.Marshal(map[string]any{"items": []string{"create calculator as a webpage"}})
	if _, observation := runtime.executeTodoWrite(input); !strings.Contains(observation, "Todo plan") {
		t.Fatalf("expected todo_write observation, got %s", observation)
	}
	list, err := runtime.Tasks.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) < 5 {
		t.Fatalf("expected calculator webpage to expand into >=5 tasks, got %d: %#v", len(list), list)
	}
	for _, task := range list {
		if !strings.Contains(task.Title, ".html") {
			t.Fatalf("expected every expanded task to reference an .html file, got %q", task.Title)
		}
	}
}

func TestTodoWriteKeepsTypoFixSingle(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	runtime := newTestRuntime(t, cwd, cfg, tools.NewRegistry(), llm.NewRegistry())

	input, _ := json.Marshal(map[string]any{"items": []string{"Fix typo in handler.go"}})
	if _, observation := runtime.executeTodoWrite(input); !strings.Contains(observation, "Todo plan") {
		t.Fatalf("expected todo_write observation, got %s", observation)
	}
	list, err := runtime.Tasks.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Title != "Fix typo in handler.go" {
		t.Fatalf("expected typo fix to remain a single task, got %#v", list)
	}
}

func TestInferImplicitTargetFile(t *testing.T) {
	cases := []struct {
		title string
		want  string
	}{
		{"create snake game in html", "snake-game.html"},
		{"build a chat ui as a webpage", "chat-ui.html"},
		{"implement a todo app in react", "App.tsx"},
		{"write a calculator using vanilla js", "calculator.js"},
		{"refactor handler.go", ""},
		{"create snake.html", ""}, // filename literal handled by caller, not this fallback
		{"create index.html", ""}, // ditto
		{"draw something with a stylesheet", "styles.css"},
	}
	for _, tc := range cases {
		got := inferImplicitTargetFile(tc.title)
		if got != tc.want {
			t.Errorf("inferImplicitTargetFile(%q) = %q, want %q", tc.title, got, tc.want)
		}
	}
}

func TestPreviewPromptIncludesArtifactStrategy(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	providers := llm.NewRegistry()
	providers.Register(&fakeProvider{responses: []string{"done"}})
	runtime := newTestRuntime(t, cwd, cfg, registry, providers)

	preview, err := runtime.PreviewPrompt("Create index.html")
	if err != nil {
		t.Fatal(err)
	}
	if preview.ArtifactStrategy != fileStrategyOneShotArtifact {
		t.Fatalf("expected one-shot preview strategy, got %#v", preview)
	}
	if !strings.Contains(preview.User, "=== USER REQUEST ===\nCreate index.html") {
		t.Fatalf("expected user request in preview, got:\n%s", preview.User)
	}
	if !strings.Contains(preview.System, "/mode build") {
		t.Fatalf("plan-mode system prompt should hand off to build mode, got:\n%s", preview.System)
	}
}
