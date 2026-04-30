package agent

import (
	"fmt"
	"sort"
)

type ToolDecision string

const (
	ToolAllow ToolDecision = "allow"
	ToolAsk   ToolDecision = "ask"
	ToolDeny  ToolDecision = "deny"
)

type SprintPolicy struct {
	allowed map[string]bool
	ask     map[string]bool
}

func NewSprintPolicy() SprintPolicy {
	allowed := map[string]bool{}
	for _, name := range []string{
		"read_file",
		"list_files",
		"search_text",
		"search_files",
		"git_status",
		"git_diff",
		"plan_write",
		"plan_get",
		"todo_write",
		"spawn_subagent",
		"spawn_subagents",
		"execute_task",
		"task_create",
		"task_list",
		"task_get",
		"task_update",
		"ask_user",
		"run_skill",
	} {
		allowed[name] = true
	}
	ask := map[string]bool{}
	for _, name := range []string{
		"edit_file",
		"write_file",
		"apply_patch",
		"run_command",
		"powershell_command",
	} {
		ask[name] = true
	}
	return SprintPolicy{allowed: allowed, ask: ask}
}

func NewReadOnlyPolicy() SprintPolicy {
	return NewSprintPolicy()
}

func NewPlanPolicy() SprintPolicy {
	allowed := map[string]bool{}
	for _, name := range []string{
		"read_file", "list_files", "search_text", "search_files",
		"git_status", "git_diff",
		"plan_write", "plan_get",
		"todo_write",
		"task_create", "task_list", "task_get", "task_update",
		"ask_user",
	} {
		allowed[name] = true
	}
	return SprintPolicy{allowed: allowed, ask: map[string]bool{}}
}

// NewBuildPolicy returns the policy for the executor mode: read tools allowed
// outright, mutating tools require per-call approval, and planning/dispatch
// tools are denied so the executor cannot re-plan or recurse into subagents.
func NewBuildPolicy() SprintPolicy {
	allowed := map[string]bool{}
	for _, name := range []string{
		"read_file", "list_files", "search_text", "search_files",
		"git_status", "git_diff",
		"plan_get",
		"task_list", "task_get", "task_update",
		"ask_user",
	} {
		allowed[name] = true
	}
	ask := map[string]bool{}
	for _, name := range []string{
		"edit_file", "write_file", "apply_patch",
		"run_command", "powershell_command",
	} {
		ask[name] = true
	}
	return SprintPolicy{allowed: allowed, ask: ask}
}

// WithInlineEdits is a no-op shim retained for backwards compatibility with
// the InlineBuilder config flag. Editing now lives in build mode; plan mode
// never gains editor tools regardless of the flag.
func WithInlineEdits(p SprintPolicy) SprintPolicy {
	return p
}

func NewExplorePolicy() SprintPolicy {
	allowed := map[string]bool{}
	for _, name := range []string{
		"read_file", "list_files", "search_text", "search_files",
		"git_status", "git_diff",
		// Read-only fan-out is consistent with explore's purpose: the
		// subagents are themselves bound to read-only tools, so letting
		// the main explorer delegate parallel analysis doesn't violate
		// the mode's safety envelope.
		"spawn_subagent", "spawn_subagents",
	} {
		allowed[name] = true
	}
	return SprintPolicy{allowed: allowed, ask: map[string]bool{}}
}

func NewReviewPolicy() SprintPolicy {
	allowed := map[string]bool{}
	for _, name := range []string{
		"read_file", "list_files", "search_text", "search_files",
		"git_status", "git_diff",
		"plan_write", "plan_get",
		"todo_write", "spawn_subagent", "spawn_subagents",
	} {
		allowed[name] = true
	}
	return SprintPolicy{allowed: allowed, ask: map[string]bool{}}
}

func (p SprintPolicy) Decision(toolName string) (ToolDecision, string) {
	if p.allowed[toolName] {
		return ToolAllow, ""
	}
	if p.ask[toolName] {
		return ToolAsk, "approval required"
	}
	return ToolDeny, fmt.Sprintf("denied by sprint policy: %s is not available in this sprint", toolName)
}

func (p SprintPolicy) Allowed(toolName string) (bool, string) {
	decision, reason := p.Decision(toolName)
	return decision == ToolAllow, reason
}

// AllowedNames returns tool names with ToolAllow decision.
func (p SprintPolicy) AllowedNames() []string {
	var names []string
	for name := range p.allowed {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// AskNames returns tool names with ToolAsk decision.
func (p SprintPolicy) AskNames() []string {
	var names []string
	for name := range p.ask {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
