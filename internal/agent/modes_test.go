package agent

import (
	"strings"
	"testing"

	"forge/internal/config"
	"forge/internal/llm"
	"forge/internal/plans"
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
	// Mutating tools and the executor checklist tools must stay denied.
	// plan_write and plan_get are now allowed because explore produces a
	// structured findings document (the auto-handoff to plan mode); see
	// promoteExplorePlanToHandoff in runtime_exec.go.
	for _, tool := range []string{"edit_file", "write_file", "run_command", "todo_write", "task_create", "task_update"} {
		decision, _ := mode.Policy.Decision(tool)
		if decision != ToolDeny {
			t.Fatalf("explore mode should deny %s, got %s", tool, decision)
		}
	}
	for _, tool := range []string{"read_file", "list_files", "search_text", "git_status", "spawn_subagent", "spawn_subagents", "plan_write", "plan_get"} {
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
		sp := systemPrompt(native, "build", "", NewBuildPolicy())
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
	sp := systemPrompt(false, "build", "", NewBuildPolicy())
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
	sp := systemPrompt(false, "plan", "", NewPlanPolicy())
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
	sp := systemPrompt(false, "chat", "", NewChatPolicy())
	if !strings.Contains(sp, "You are in chat mode") {
		t.Fatalf("chat-mode prompt missing chat instructions:\n%s", sp)
	}
	if strings.Contains(sp, `"name":"plan_write"`) {
		t.Fatalf("chat-mode prompt should not advertise planning tools:\n%s", sp)
	}
}

// TestDetectPlanVariant pins the priority and the trigger conditions
// for the three variants. Refine wins over from_explore when both
// signals are present — the existing plan represents committed
// decisions that must survive new findings.
func TestDetectPlanVariant(t *testing.T) {
	t.Run("not in plan mode returns empty", func(t *testing.T) {
		cwd := t.TempDir()
		r := newTestRuntime(t, cwd, config.Defaults(), tools.NewRegistry(), llm.NewRegistry())
		_ = r.SetMode("build")
		if v := r.detectPlanVariant(); v != "" {
			t.Errorf("non-plan mode should return empty variant, got %q", v)
		}
	})

	t.Run("plan with no signals returns cold", func(t *testing.T) {
		cwd := t.TempDir()
		r := newTestRuntime(t, cwd, config.Defaults(), tools.NewRegistry(), llm.NewRegistry())
		// default mode is plan; no plan, no tasks, no PendingExplorerContext
		if v := r.detectPlanVariant(); v != PlanVariantCold {
			t.Errorf("plan with no signals = cold, got %q", v)
		}
	})

	t.Run("explorer findings present returns from_explore", func(t *testing.T) {
		cwd := t.TempDir()
		r := newTestRuntime(t, cwd, config.Defaults(), tools.NewRegistry(), llm.NewRegistry())
		r.PendingExplorerContext = "Summary: explored combat log\nStubs:\n- src/Game.tsx:142 (combat.log calls)"
		if v := r.detectPlanVariant(); v != PlanVariantFromExplore {
			t.Errorf("PendingExplorerContext set = from_explore, got %q", v)
		}
	})

	t.Run("active plan with pending tasks returns refine", func(t *testing.T) {
		cwd := t.TempDir()
		r := newTestRuntime(t, cwd, config.Defaults(), tools.NewRegistry(), llm.NewRegistry())
		// Save a plan + add a pending task.
		_, _ = r.Plans.Save(plans.Document{Summary: "approved goal", Approach: "do the thing"})
		_, _ = r.Tasks.Create("Fix src/Game.tsx", "")
		if v := r.detectPlanVariant(); v != PlanVariantRefine {
			t.Errorf("plan + pending tasks = refine, got %q", v)
		}
	})

	t.Run("refine wins over from_explore when both present", func(t *testing.T) {
		cwd := t.TempDir()
		r := newTestRuntime(t, cwd, config.Defaults(), tools.NewRegistry(), llm.NewRegistry())
		_, _ = r.Plans.Save(plans.Document{Summary: "approved goal", Approach: "do the thing"})
		_, _ = r.Tasks.Create("Fix src/Game.tsx", "")
		r.PendingExplorerContext = "fresh findings from a second explore pass"
		if v := r.detectPlanVariant(); v != PlanVariantRefine {
			t.Errorf("plan+tasks+PendingExplorerContext = refine wins, got %q", v)
		}
	})

	t.Run("plan with all tasks completed returns cold", func(t *testing.T) {
		cwd := t.TempDir()
		r := newTestRuntime(t, cwd, config.Defaults(), tools.NewRegistry(), llm.NewRegistry())
		_, _ = r.Plans.Save(plans.Document{Summary: "done goal", Approach: "did the thing"})
		task, err := r.Tasks.Create("Fix src/Game.tsx", "")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = r.Tasks.Update(task.ID, "", "completed", "")
		// All tasks completed → no active work → cold (not refine)
		if v := r.detectPlanVariant(); v != PlanVariantCold {
			t.Errorf("plan with all tasks completed = cold, got %q", v)
		}
	})
}

// TestPlanPromptVariantsDifferMeaningfully pins the contract: each plan
// variant produces a different system prompt, and the language reflects
// the upstream context.
func TestPlanPromptVariantsDifferMeaningfully(t *testing.T) {
	cold := PlanPromptForVariant(PlanVariantCold)
	fromExplore := PlanPromptForVariant(PlanVariantFromExplore)
	refine := PlanPromptForVariant(PlanVariantRefine)

	if cold == fromExplore || cold == refine || fromExplore == refine {
		t.Fatal("plan variants should produce distinct prompts; got duplicates")
	}

	// from_explore must explicitly tell the model NOT to re-investigate.
	if !strings.Contains(fromExplore, "DESIGN, not investigation") {
		t.Errorf("from_explore prompt should emphasize design over investigation:\n%s", fromExplore)
	}
	if !strings.Contains(strings.ToLower(fromExplore), "skip ask_user") {
		t.Errorf("from_explore prompt should tell model to skip ask_user:\n%s", fromExplore)
	}
	// refine must explicitly tell the model NOT to clobber the existing plan.
	if !strings.Contains(refine, "PRESERVE") {
		t.Errorf("refine prompt should emphasize preservation:\n%s", refine)
	}
	if !strings.Contains(strings.ToLower(refine), "destructive") {
		t.Errorf("refine prompt should warn about destructive overwrites:\n%s", refine)
	}
	// Cold prompt is the default — should still mention ask_user as STEP 1.
	if !strings.Contains(cold, "ask_user") {
		t.Errorf("cold prompt should still call out ask_user:\n%s", cold)
	}

	// Empty / unknown variant falls back to cold.
	if PlanPromptForVariant("") != cold {
		t.Errorf("empty variant should fall back to cold")
	}
	if PlanPromptForVariant("nonexistent") != cold {
		t.Errorf("unknown variant should fall back to cold")
	}
}

// TestSystemPromptCacheKeyIncludesVariant confirms different plan
// variants produce different cache keys — without this the cache would
// serve a stale variant prompt across turn transitions.
func TestSystemPromptCacheKeyIncludesVariant(t *testing.T) {
	policy := NewPlanPolicy()
	cold := systemPromptCacheKey(true, "plan", PlanVariantCold, policy)
	fromExplore := systemPromptCacheKey(true, "plan", PlanVariantFromExplore, policy)
	refine := systemPromptCacheKey(true, "plan", PlanVariantRefine, policy)
	noVariant := systemPromptCacheKey(true, "plan", "", policy)

	if cold == fromExplore || cold == refine || fromExplore == refine {
		t.Fatalf("cache keys should differ by variant; got cold=%q fromExplore=%q refine=%q", cold, fromExplore, refine)
	}
	if noVariant == cold {
		t.Errorf("empty variant key should NOT collide with explicit cold variant key — got both=%q", cold)
	}

	// Build mode (variant-irrelevant) should produce identical keys.
	build1 := systemPromptCacheKey(true, "build", "", NewBuildPolicy())
	build2 := systemPromptCacheKey(true, "build", "", NewBuildPolicy())
	if build1 != build2 {
		t.Errorf("identical (mode, variant, policy) should produce identical keys; got %q vs %q", build1, build2)
	}
}
