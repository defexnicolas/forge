package agent

import (
	"testing"

	"forge/internal/config"
	"forge/internal/llm"
	"forge/internal/tools"
)

func TestAllModesExist(t *testing.T) {
	for _, name := range []string{"build", "plan", "explore"} {
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

func TestModeNames(t *testing.T) {
	names := ModeNames()
	if len(names) != 3 {
		t.Fatalf("expected 3 modes, got %d", len(names))
	}
	// Should be sorted alphabetically.
	expected := []string{"build", "explore", "plan"}
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
	// But allows reads
	for _, tool := range []string{"read_file", "search_text", "todo_write"} {
		decision, _ := mode.Policy.Decision(tool)
		if decision != ToolAllow {
			t.Fatalf("plan mode should allow %s, got %s", tool, decision)
		}
	}
}

func TestExploreModeDeniesEverythingExceptReads(t *testing.T) {
	mode, _ := GetMode("explore")
	for _, tool := range []string{"edit_file", "write_file", "run_command", "spawn_subagent", "todo_write"} {
		decision, _ := mode.Policy.Decision(tool)
		if decision != ToolDeny {
			t.Fatalf("explore mode should deny %s, got %s", tool, decision)
		}
	}
	for _, tool := range []string{"read_file", "list_files", "search_text", "git_status"} {
		decision, _ := mode.Policy.Decision(tool)
		if decision != ToolAllow {
			t.Fatalf("explore mode should allow %s, got %s", tool, decision)
		}
	}
}

// review, commit, debug are now subagents, not modes.

func TestBuildModeAsksForEdits(t *testing.T) {
	mode, _ := GetMode("build")
	decision, _ := mode.Policy.Decision("edit_file")
	if decision != ToolAsk {
		t.Fatalf("build mode should ask for edit_file, got %s", decision)
	}
	decision, _ = mode.Policy.Decision("read_file")
	if decision != ToolAllow {
		t.Fatalf("build mode should allow read_file, got %s", decision)
	}
}

func TestSetModeValid(t *testing.T) {
	cwd := t.TempDir()
	runtime := NewRuntime(cwd, config.Defaults(), tools.NewRegistry(), llm.NewRegistry())
	if runtime.Mode != "build" {
		t.Fatalf("default mode should be build, got %s", runtime.Mode)
	}
	if err := runtime.SetMode("plan"); err != nil {
		t.Fatal(err)
	}
	if runtime.Mode != "plan" {
		t.Fatalf("expected plan, got %s", runtime.Mode)
	}
	// Plan mode should deny edits
	decision, _ := runtime.Policy.Decision("edit_file")
	if decision != ToolDeny {
		t.Fatalf("after SetMode(plan), edit_file should be denied, got %s", decision)
	}
}

func TestSetModeInvalid(t *testing.T) {
	cwd := t.TempDir()
	runtime := NewRuntime(cwd, config.Defaults(), tools.NewRegistry(), llm.NewRegistry())
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
