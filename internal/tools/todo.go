package tools

import (
	"encoding/json"
	"strings"
)

type todoWriteTool struct{}

func (todoWriteTool) Name() string { return "todo_write" }
func (todoWriteTool) Description() string {
	return "Replace the visible executable checklist. Use plan_write for the full plan document. Each item MUST be a concrete, verifiable action — name the target file or component, the specific change, and how you'll know it worked. Vague items like 'Fix all combat.log' or 'Review remaining file' are forbidden: count first (search_text or read_file), then create N specific items, OR one item that names the count and file explicitly. Good: 'Replace the 12 combat.log calls in src/Game.tsx with console.log; verify with grep'. Bad: 'Fix combat.log calls'."
}
func (todoWriteTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","required":["items"],"properties":{"items":{"oneOf":[{"type":"array","items":{"type":"string"}},{"type":"array","items":{"type":"object","required":["title"],"properties":{"title":{"type":"string"},"status":{"type":"string","enum":["pending","in_progress","completed"]},"notes":{"type":"string"}}}}],"description":"Task titles. Each title must reference a concrete file/component AND a verifiable acceptance condition. Prefer simple strings: [\"Replace 12 combat.log calls in src/Game.tsx with console.log; verify with grep\",\"Fix entry.itemId.includes() in src/loot.ts:34 by checking string equality instead\"]. Objects also accepted: [{\"title\":\"...\",\"status\":\"completed\"}]."}}}`)
}
func (todoWriteTool) Permission(Context, json.RawMessage) PermissionRequest {
	return PermissionRequest{Decision: PermissionAllow}
}
func (todoWriteTool) Run(ctx Context, input json.RawMessage) (Result, error) {
	var req struct {
		Items []string `json:"items"`
	}
	if err := json.Unmarshal(input, &req); err != nil {
		return Result{}, err
	}
	clean := make([]string, 0, len(req.Items))
	for _, raw := range req.Items {
		if t := normalizeTaskTitle(raw); t != "" {
			clean = append(clean, t)
		}
	}
	return Result{
		Title:   "Todo plan",
		Summary: "Updated plan",
		Content: []ContentBlock{{Type: "text", Text: strings.Join(clean, "\n")}},
	}, nil
}

// normalizeTaskTitle collapses embedded whitespace (newlines + runs of
// spaces) in a task title down to a single space. Models occasionally
// emit "[ \"Fix X with stuff,\\n      and more stuff,\\n      and...\" ]"
// which renders as a multi-line task title with random indentation when
// the runtime later wraps the content for display — the right viewport
// looks broken even though the underlying data is fine. Stripping at
// the tool boundary keeps everything downstream (plan panel, tool
// result echo, transcript) working with single-line titles regardless
// of how the model formats its JSON string literals.
func normalizeTaskTitle(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	return strings.Join(strings.Fields(s), " ")
}

// Task tool shells. The actual implementation lives in agent/runtime_exec.go
// (runTaskTool) — the runtime intercepts task_* calls before they hit the
// registry. These shells exist so the model sees real schemas + descriptions
// in the ToolDef list instead of generic stubs.
type taskCreateTool struct{}

func (taskCreateTool) Name() string { return "task_create" }
func (taskCreateTool) Description() string {
	return "Create a new task in the session plan. Use this to add individual items without replacing the whole plan. The title MUST name (a) the target file or component, (b) the specific change, and (c) the verification step. Vague titles like 'Fix all X' or 'Review remaining' are forbidden — count occurrences first, then create one task per occurrence (or one task that explicitly states the count + file + verification). Good: 'Replace setEmpty() with reset() in src/initEmp.ts; verify with grep -c setEmpty src/'. Bad: 'Fix initEmp'."
}
func (taskCreateTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","required":["title"],"properties":{"title":{"type":"string","description":"Concrete, verifiable task title. MUST include target file/component + specific change + how you'll verify (build, test, grep, etc)."},"notes":{"type":"string","description":"Optional notes / acceptance criteria. Use this for line numbers, expected diff size, or which test to re-run."}}}`)
}
func (taskCreateTool) Permission(Context, json.RawMessage) PermissionRequest {
	return PermissionRequest{Decision: PermissionAllow}
}
func (taskCreateTool) Run(Context, json.RawMessage) (Result, error) {
	return Result{Title: "task_create", Summary: "dispatched via runtime"}, nil
}

type taskListTool struct{}

func (taskListTool) Name() string { return "task_list" }
func (taskListTool) Description() string {
	return "List all tasks in the session plan with their IDs and statuses. Call this to read the current plan instead of relying on injected context."
}
func (taskListTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (taskListTool) Permission(Context, json.RawMessage) PermissionRequest {
	return PermissionRequest{Decision: PermissionAllow}
}
func (taskListTool) Run(Context, json.RawMessage) (Result, error) {
	return Result{Title: "task_list", Summary: "dispatched via runtime"}, nil
}

type taskGetTool struct{}

func (taskGetTool) Name() string { return "task_get" }
func (taskGetTool) Description() string {
	return "Get full details for a single task by ID."
}
func (taskGetTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","required":["id"],"properties":{"id":{"type":"string"}}}`)
}
func (taskGetTool) Permission(Context, json.RawMessage) PermissionRequest {
	return PermissionRequest{Decision: PermissionAllow}
}
func (taskGetTool) Run(Context, json.RawMessage) (Result, error) {
	return Result{Title: "task_get", Summary: "dispatched via runtime"}, nil
}

type taskUpdateTool struct{}

func (taskUpdateTool) Name() string { return "task_update" }
func (taskUpdateTool) Description() string {
	return "Update a task's status (pending|in_progress|completed), title, or notes. Prefer id when available; if id is omitted, title may be used to target an existing task during plan refinement."
}
func (taskUpdateTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"id":{"type":"string","description":"Preferred stable task ID, e.g. plan-1"},"title":{"type":"string","description":"Task title. If id is omitted, Forge will try to match an existing task by title."},"status":{"type":"string","enum":["pending","in_progress","completed","cancelled"]},"notes":{"type":"string"}}}`)
}
func (taskUpdateTool) Permission(Context, json.RawMessage) PermissionRequest {
	return PermissionRequest{Decision: PermissionAllow}
}
func (taskUpdateTool) Run(Context, json.RawMessage) (Result, error) {
	return Result{Title: "task_update", Summary: "dispatched via runtime"}, nil
}

type noopTool struct {
	name        string
	description string
}

func (t noopTool) Name() string            { return t.name }
func (t noopTool) Description() string     { return t.description }
func (t noopTool) Status() string          { return "stub" }
func (t noopTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t noopTool) Permission(Context, json.RawMessage) PermissionRequest {
	return PermissionRequest{Decision: PermissionAsk, Reason: "stub tool requires explicit approval"}
}
func (t noopTool) Run(Context, json.RawMessage) (Result, error) {
	return Result{Title: t.name, Summary: "Stub tool registered for compatibility; implementation pending."}, nil
}
