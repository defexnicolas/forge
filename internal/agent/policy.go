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
		"read_files",
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
		"read_file", "read_files", "list_files", "search_text", "search_files",
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

func NewChatPolicy() SprintPolicy {
	allowed := map[string]bool{}
	for _, name := range []string{
		"read_file", "read_files", "list_files", "search_text", "search_files",
		"git_status", "git_diff",
		"ask_user",
	} {
		allowed[name] = true
	}
	return SprintPolicy{allowed: allowed, ask: map[string]bool{}}
}

// NewBuildPolicy returns the policy for the executor mode: read tools allowed
// outright, mutating tools require per-call approval, and planning tools that
// would let the executor REWRITE the plan (plan_write, todo_write) stay
// denied so the executor can't loop on re-plans. task_create IS allowed
// because the model often discovers new sub-work mid-implementation
// ("oh, this also depends on Z") and without an externalising tool it
// carries the discovery in prose, forgets, and re-rediscovers — a real
// driver of the read-then-narrate loops we've been chasing. Letting the
// executor add a task is strictly additive: it never replaces what the
// planner produced.
func NewBuildPolicy() SprintPolicy {
	allowed := map[string]bool{}
	for _, name := range []string{
		"read_file", "read_files", "list_files", "search_text", "search_files",
		"git_status", "git_diff",
		"plan_get",
		"task_create", "task_list", "task_get", "task_update",
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
		"read_file", "read_files", "list_files", "search_text", "search_files",
		"git_status", "git_diff",
		// Read-only fan-out is consistent with explore's purpose: the
		// subagents are themselves bound to read-only tools, so letting
		// the main explorer delegate parallel analysis doesn't violate
		// the mode's safety envelope.
		"spawn_subagent", "spawn_subagents",
		// plan_write is the structured handoff to plan mode. Explore
		// produces the findings document (context, assumptions, stubs,
		// risks); plan mode picks it up via PendingExplorerContext on
		// the next turn. plan_get is also allowed so the model can read
		// any pre-existing plan to avoid clobbering. todo_write and the
		// task_* tools are deliberately excluded — designing the
		// checklist is plan mode's job, not explore's.
		"plan_write", "plan_get",
	} {
		allowed[name] = true
	}
	return SprintPolicy{allowed: allowed, ask: map[string]bool{}}
}

// NewDebugPolicy is the policy for the dedicated debug MODE (separate from
// the read-only debug subagent). Debugging requires INSTRUMENTATION
// (adding console.log / print / debug_assert) and EXECUTION of the code
// under test — capabilities a read-only mode cannot provide. The policy
// allows:
//   - the full read-only investigation suite
//   - edit_file / write_file / apply_patch (instrumentation; under
//     approval like build mode so risky edits surface)
//   - run_command (run the program; under approval)
//   - web_fetch (hit localhost servers the user starts during a debug
//     cycle — no approval, network reads are non-destructive)
//   - task_list / task_get / plan_get (read-only on planning state for
//     orientation; debug mode is not allowed to mutate the plan or
//     checklist — that's plan mode's job)
//   - ask_user (can request reproduction steps when the bug isn't clear)
//
// Explicitly denied: plan_write, todo_write, task_create, task_update,
// execute_task, spawn_subagent. Debug mode does not design solutions or
// dispatch work — its single job is finding the root cause and
// applying the minimal fix in-place. Once the fix is verified the user
// can switch to plan/build for follow-up work if the change implies a
// larger refactor.
func NewDebugPolicy() SprintPolicy {
	allowed := map[string]bool{}
	for _, name := range []string{
		"read_file", "read_files", "list_files", "search_text", "search_files",
		"git_status", "git_diff",
		"plan_get", "task_list", "task_get",
		"ask_user",
		"web_fetch",
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

func NewReviewPolicy() SprintPolicy {
	allowed := map[string]bool{}
	for _, name := range []string{
		"read_file", "read_files", "list_files", "search_text", "search_files",
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
