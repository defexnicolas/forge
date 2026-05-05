package agent

import (
	"strings"
	"testing"

	"forge/internal/config"
	"forge/internal/llm"
	"forge/internal/tools"
)

func TestAllModesExist(t *testing.T) {
	for _, name := range []string{"chat", "plan", "build", "explore"} {
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
	if len(names) != 4 {
		t.Fatalf("expected 4 modes (chat + plan + build + explore), got %d: %v", len(names), names)
	}
	expected := []string{"build", "chat", "explore", "plan"}
	for i, name := range expected {
		if names[i] != name {
			t.Fatalf("expected mode %d to be %s, got %s (full: %v)", i, name, names[i], names)
		}
	}
}

func TestChatModeAllowsOnlyReadConversationTools(t *testing.T) {
	mode, _ := GetMode("chat")
	for _, tool := range []string{"edit_file", "write_file", "apply_patch", "run_command", "todo_write", "plan_write", "plan_get"} {
		decision, _ := mode.Policy.Decision(tool)
		if decision != ToolDeny {
			t.Fatalf("chat mode should deny %s, got %s", tool, decision)
		}
	}
	for _, tool := range []string{"read_file", "list_files", "search_text", "git_status", "ask_user"} {
		decision, _ := mode.Policy.Decision(tool)
		if decision != ToolAllow {
			t.Fatalf("chat mode should allow %s, got %s", tool, decision)
		}
	}
}

func TestPlanModeDeniesEdits(t *testing.T) {
	mode, _ := GetMode("plan")
	for _, tool := range []string{"edit_file", "write_file", "apply_patch", "run_command", "execute_task", "spawn_subagent", "spawn_subagents"} {
		decision, _ := mode.Policy.Decision(tool)
		if decision != ToolDeny {
			t.Fatalf("plan mode should deny %s, got %s", tool, decision)
		}
	}
	for _, tool := range []string{"read_file", "search_text", "plan_write", "plan_get", "todo_write", "task_list", "task_update", "ask_user"} {
		decision, _ := mode.Policy.Decision(tool)
		if decision != ToolAllow {
			t.Fatalf("plan mode should allow %s, got %s", tool, decision)
		}
	}
}

func TestBuildModeAllowsEditsUnderApproval(t *testing.T) {
	mode, _ := GetMode("build")
	for _, tool := range []string{"edit_file", "write_file", "apply_patch", "run_command", "powershell_command"} {
		decision, _ := mode.Policy.Decision(tool)
		if decision != ToolAsk {
			t.Fatalf("build mode should require approval for %s, got %s", tool, decision)
		}
	}
	for _, tool := range []string{"read_file", "list_files", "search_text", "plan_get", "task_list", "task_update"} {
		decision, _ := mode.Policy.Decision(tool)
		if decision != ToolAllow {
			t.Fatalf("build mode should allow %s, got %s", tool, decision)
		}
	}
	for _, tool := range []string{"plan_write", "todo_write", "execute_task", "spawn_subagent", "spawn_subagents"} {
		decision, _ := mode.Policy.Decision(tool)
		if decision != ToolDeny {
			t.Fatalf("build mode should deny %s (executor must not re-plan or recurse), got %s", tool, decision)
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
	if err := runtime.SetMode("chat"); err != nil {
		t.Fatal(err)
	}
	if runtime.Mode != "chat" {
		t.Fatalf("expected chat, got %s", runtime.Mode)
	}
	if err := runtime.SetMode("build"); err != nil {
		t.Fatal(err)
	}
	if runtime.Mode != "build" {
		t.Fatalf("SetMode(build) should switch to build, got %s", runtime.Mode)
	}
	if err := runtime.SetMode("plan"); err != nil {
		t.Fatal(err)
	}
	if runtime.Mode != "plan" {
		t.Fatalf("expected plan, got %s", runtime.Mode)
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

func TestSystemPromptIncludesBuildModeInstructions(t *testing.T) {
	for _, native := range []bool{true, false} {
		sp := systemPrompt(native, "build", NewBuildPolicy())
		if !strings.Contains(sp, "You are in build mode") {
			t.Fatalf("native=%v: build-mode prompt missing build-mode instructions:\n%s", native, sp)
		}
		if !strings.Contains(sp, "task_update") {
			t.Fatalf("native=%v: build-mode prompt should mention task_update", native)
		}
		if !strings.Contains(sp, "Only call plan_get or task_list if that digest is insufficient") {
			t.Fatalf("native=%v: build-mode prompt should prefer in-prompt digest before plan_get/task_list:\n%s", native, sp)
		}
		if !strings.Contains(sp, "Do NOT call execute_task") {
			t.Fatalf("native=%v: build-mode prompt should forbid execute_task", native)
		}
		if !strings.Contains(sp, "Do NOT narrate your understanding") {
			t.Fatalf("native=%v: build-mode prompt should forbid prose summaries while tasks remain", native)
		}
	}
}

func TestSystemPromptHidesDeniedToolExamplesInBuildMode(t *testing.T) {
	sp := systemPrompt(false, "build", NewBuildPolicy())
	// task_create IS allowed in build mode (added so the executor can
	// externalise newly-discovered work mid-implementation rather than
	// looping in prose). plan_write / todo_write / spawn_subagent stay
	// denied — those would let the executor re-plan or recurse, which
	// is the planner's job.
	for _, denied := range []string{`"name":"plan_write"`, `"name":"todo_write"`, `"name":"spawn_subagent"`} {
		if strings.Contains(sp, denied) {
			t.Fatalf("build-mode prompt should not advertise %s (build denies it):\n%s", denied, sp)
		}
	}
	if !strings.Contains(sp, `"name":"task_create"`) {
		t.Fatalf("build-mode prompt should ADVERTISE task_create (now allowed):\n%s", sp)
	}
}

func TestSystemPromptKeepsExamplesInPlanMode(t *testing.T) {
	sp := systemPrompt(false, "plan", NewPlanPolicy())
	for _, allowed := range []string{`"name":"plan_write"`, `"name":"task_create"`, `"name":"task_update"`} {
		if !strings.Contains(sp, allowed) {
			t.Fatalf("plan-mode prompt should still show %s example:\n%s", allowed, sp)
		}
	}
	if !strings.Contains(sp, "Use todo_write only when starting from scratch") {
		t.Fatal("plan-mode prompt should keep the todo_write usage note")
	}
}

func TestSystemPromptIncludesChatModeInstructions(t *testing.T) {
	sp := systemPrompt(false, "chat", NewChatPolicy())
	if !strings.Contains(sp, "You are in chat mode") {
		t.Fatalf("chat-mode prompt missing chat instructions:\n%s", sp)
	}
	if strings.Contains(sp, `"name":"plan_write"`) {
		t.Fatalf("chat-mode prompt should not advertise planning tools:\n%s", sp)
	}
}
