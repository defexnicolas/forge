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
