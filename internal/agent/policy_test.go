package agent

import "testing"

func TestSprintPolicy(t *testing.T) {
	policy := NewSprintPolicy()
	for _, name := range []string{"read_file", "list_files", "search_text", "search_files", "git_status", "git_diff", "plan_write", "plan_get", "todo_write", "spawn_subagents"} {
		if decision, reason := policy.Decision(name); decision != ToolAllow {
			t.Fatalf("expected %s allowed, got %s", name, reason)
		}
	}
	for _, name := range []string{"edit_file", "write_file", "apply_patch", "run_command"} {
		if decision, reason := policy.Decision(name); decision != ToolAsk {
			t.Fatalf("expected %s ask, got %s (%s)", name, decision, reason)
		}
	}
	for _, name := range []string{"external_tool", "list_mcp_resources"} {
		if decision, _ := policy.Decision(name); decision != ToolDeny {
			t.Fatalf("expected %s denied", name)
		}
	}
}

// TestDebugPolicyAllowsExplorerSpawn pins that debug mode can call
// spawn_subagent at the policy layer (the runtime then enforces
// explorer-only and one-per-turn). spawn_subagents stays denied because
// debug investigation is sequential, and execute_task / plan_write stay
// denied because debug does not design or dispatch.
func TestDebugPolicyAllowsExplorerSpawn(t *testing.T) {
	policy := NewDebugPolicy()
	if decision, reason := policy.Decision("spawn_subagent"); decision != ToolAllow {
		t.Fatalf("expected spawn_subagent allowed in debug, got %s (%s)", decision, reason)
	}
	for _, name := range []string{"spawn_subagents", "execute_task", "plan_write", "todo_write", "task_create", "task_update"} {
		if decision, _ := policy.Decision(name); decision != ToolDeny {
			t.Fatalf("expected %s denied in debug, got %s", name, decision)
		}
	}
	for _, name := range []string{"edit_file", "write_file", "apply_patch", "run_command", "powershell_command"} {
		if decision, _ := policy.Decision(name); decision != ToolAsk {
			t.Fatalf("expected %s under approval in debug, got %s", name, decision)
		}
	}
	for _, name := range []string{"read_file", "list_files", "search_text", "search_files", "ask_user", "web_fetch"} {
		if decision, _ := policy.Decision(name); decision != ToolAllow {
			t.Fatalf("expected %s allowed in debug, got %s", name, decision)
		}
	}
}
