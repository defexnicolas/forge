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
	runtime := NewRuntime(cwd, cfg, registry, providers)

	input, _ := json.Marshal(map[string]string{"agent": "explorer", "prompt": "find tools"})
	result, observation := runtime.executeSubagent(context.Background(), input)
	if result == nil {
		t.Fatal("expected subagent result")
	}
	if !strings.Contains(observation, "found tools") {
		t.Fatalf("expected subagent observation, got %s", observation)
	}
}

func TestRuntimeTodoWriteUpdatesPlan(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	runtime := NewRuntime(cwd, cfg, registry, llm.NewRegistry())

	input, _ := json.Marshal(map[string][]string{"items": {"read code", "write tests"}})
	result, _ := runtime.executeTodoWrite(input)
	if result == nil || result.Summary != "Updated plan" {
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
	runtime := NewRuntime(cwd, cfg, tools.NewRegistry(), llm.NewRegistry())

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

func TestClaudeAliasesForSubagentsAndTasks(t *testing.T) {
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	for alias, want := range map[string]string{
		"Agent":      "spawn_subagent",
		"Task":       "spawn_subagent",
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
