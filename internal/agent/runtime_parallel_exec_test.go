package agent

import (
	"context"
	"encoding/json"
	"testing"

	"forge/internal/config"
	"forge/internal/llm"
	"forge/internal/tools"
)

// The parallel pre-executor must be opt-in: it only fires for homogeneous
// execute_task batches in build mode with concurrency > 1. Every other shape
// must return nil so the sequential dispatch loop runs unchanged.
func TestMaybePreExecuteParallelGuards(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Build.Subagents.Concurrency = 2
	r := newTestRuntime(t, cwd, cfg, tools.NewRegistry(), llm.NewRegistry())

	twoExec := []llm.ToolCall{
		{ID: "a", Type: "function", Function: llm.FunctionCall{Name: "execute_task", Arguments: `{"task_id":"1"}`}},
		{ID: "b", Type: "function", Function: llm.FunctionCall{Name: "execute_task", Arguments: `{"task_id":"2"}`}},
	}

	t.Run("plan mode disables parallel", func(t *testing.T) {
		r.Mode = "plan"
		if got := r.maybePreExecuteParallelExecuteTasks(context.Background(), twoExec, nil); got != nil {
			t.Errorf("plan mode should return nil, got %v", got)
		}
	})

	t.Run("single call disables parallel", func(t *testing.T) {
		r.Mode = "build"
		one := twoExec[:1]
		if got := r.maybePreExecuteParallelExecuteTasks(context.Background(), one, nil); got != nil {
			t.Errorf("single call should return nil, got %v", got)
		}
	})

	t.Run("mixed batch disables parallel", func(t *testing.T) {
		r.Mode = "build"
		mixed := []llm.ToolCall{
			twoExec[0],
			{ID: "c", Type: "function", Function: llm.FunctionCall{Name: "read_file", Arguments: `{"path":"x"}`}},
		}
		if got := r.maybePreExecuteParallelExecuteTasks(context.Background(), mixed, nil); got != nil {
			t.Errorf("mixed batch should return nil, got %v", got)
		}
	})

	t.Run("concurrency 1 disables parallel", func(t *testing.T) {
		r.Mode = "build"
		r.Config.Build.Subagents.Concurrency = 1
		defer func() { r.Config.Build.Subagents.Concurrency = 2 }()
		if got := r.maybePreExecuteParallelExecuteTasks(context.Background(), twoExec, nil); got != nil {
			t.Errorf("concurrency 1 should return nil, got %v", got)
		}
	})
}

// FromNativeToolCall is exercised inside the helper. This sanity check
// ensures the function-name extraction path the helper relies on still
// works for the native tool-call shape.
func TestFromNativeToolCallSurfacesExecuteTask(t *testing.T) {
	tc := llm.ToolCall{
		ID:   "x",
		Type: "function",
		Function: llm.FunctionCall{
			Name:      "execute_task",
			Arguments: `{"task_id":"abc"}`,
		},
	}
	got := FromNativeToolCall(tc)
	if got.Name != "execute_task" {
		t.Fatalf("Name = %q, want execute_task", got.Name)
	}
	var parsed map[string]string
	if err := json.Unmarshal(got.Input, &parsed); err != nil {
		t.Fatalf("Input not valid JSON: %v", err)
	}
	if parsed["task_id"] != "abc" {
		t.Errorf("task_id = %q, want abc", parsed["task_id"])
	}
}
