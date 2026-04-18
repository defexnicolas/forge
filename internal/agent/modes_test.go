package agent

import (
	"testing"

	"forge/internal/config"
	"forge/internal/llm"
	"forge/internal/tools"
)

func TestAllModesExist(t *testing.T) {
	for _, name := range []string{"plan", "explore"} {
		mode, ok := GetMode(name)
		if !ok {
			t.Fatalf("mode %s should exist", name)
		}
		if mode.Name != name {
			t.Fatalf("mode name mismatch: %s vs %s", mode.Name, name)
		}
		if mode.Description == "" {
			t.Fatalf("mode %s has empty description", name)
		}
		if mode.Prompt == "" {
			t.Fatalf("mode %s has empty prompt", name)
		}
	}
}

func TestBuildModeRemoved(t *testing.T) {
	if _, ok := GetMode("build"); ok {
		t.Fatal("build mode was removed; execution now runs via the builder subagent dispatched by plan mode")
	}
}

func TestModeNames(t *testing.T) {
	names := ModeNames()
	if len(names) != 2 {
		t.Fatalf("expected 2 modes (plan + explore), got %d: %v", len(names), names)
	}
	expected := []string{"explore", "plan"}
	for i, name := range expected {
		if names[i] != name {
			t.Fatalf("expected mode %d to be %s, got %s (full: %v)", i, name, names[i], names)
		}
	}
}

func TestPlanModeDeniesEdits(t *testing.T) {
	mode, _ := GetMode("plan")
	for _, tool := range []string{"edit_file", "write_file", "apply_patch", "run_command"} {
		decision, _ := mode.Policy.Decision(tool)
		if decision != ToolDeny {
			t.Fatalf("plan mode should deny %s, got %s", tool, decision)
		}
	}
	// But allows reads + the delegation primitives.
	for _, tool := range []string{"read_file", "search_text", "plan_write", "plan_get", "todo_write", "spawn_subagents", "execute_task"} {
		decision, _ := mode.Policy.Decision(tool)
		if decision != ToolAllow {
			t.Fatalf("plan mode should allow %s, got %s", tool, decision)
		}
	}
}

func TestExploreModeDeniesEverythingExceptReads(t *testing.T) {
	mode, _ := GetMode("explore")
	for _, tool := range []string{"edit_file", "write_file", "run_command", "todo_write", "plan_get"} {
		decision, _ := mode.Policy.Decision(tool)
		if decision != ToolDeny {
			t.Fatalf("explore mode should deny %s, got %s", tool, decision)
		}
	}
	for _, tool := range []string{"read_file", "list_files", "search_text", "git_status", "spawn_subagent", "spawn_subagents"} {
		decision, _ := mode.Policy.Decision(tool)
		if decision != ToolAllow {
			t.Fatalf("explore mode should allow %s, got %s", tool, decision)
		}
	}
}

func TestSetModeValid(t *testing.T) {
	cwd := t.TempDir()
	runtime := newTestRuntime(t, cwd, config.Defaults(), tools.NewRegistry(), llm.NewRegistry())
	if runtime.Mode != "plan" {
		t.Fatalf("default mode should be plan, got %s", runtime.Mode)
	}
	if err := runtime.SetMode("explore"); err != nil {
		t.Fatal(err)
	}
	if runtime.Mode != "explore" {
		t.Fatalf("expected explore, got %s", runtime.Mode)
	}
	// Legacy "build" requests re-map silently to "plan" for backwards compat.
	if err := runtime.SetMode("build"); err != nil {
		t.Fatal(err)
	}
	if runtime.Mode != "plan" {
		t.Fatalf("SetMode(build) should re-map to plan, got %s", runtime.Mode)
	}
}

func TestSetModeInvalid(t *testing.T) {
	cwd := t.TempDir()
	runtime := newTestRuntime(t, cwd, config.Defaults(), tools.NewRegistry(), llm.NewRegistry())
	if err := runtime.SetMode("nonexistent"); err == nil {
		t.Fatal("expected error for invalid mode")
	}
}

func TestUnknownModeNotFound(t *testing.T) {
	_, ok := GetMode("nonexistent")
	if ok {
		t.Fatal("nonexistent mode should not be found")
	}
}
