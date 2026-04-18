package tools

import "encoding/json"

// executeTaskTool is a shell. The real dispatch lives in
// internal/agent/runtime_exec.go (executeTaskTool method). It delegates the
// task to the "builder" subagent with only the task text and relevant_files
// as context — the builder never sees the full plan document.
type executeTaskTool struct{}

func (executeTaskTool) Name() string { return "execute_task" }
func (executeTaskTool) Description() string {
	return "Delegate ONE checklist task to the builder subagent. Pass only the minimal context the builder needs: the task_id and the list of files likely to be read or edited. The builder runs with its own model role (editor) and respects user approval for every mutating action."
}
func (executeTaskTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","required":["task_id"],"properties":{"task_id":{"type":"string","description":"ID of the task to execute, e.g. plan-1"},"relevant_files":{"type":"array","items":{"type":"string"},"description":"Minimal list of repo paths the builder should be aware of. Keep it tight — the builder can call read_file/list_files itself if it needs more."},"notes":{"type":"string","description":"Optional extra guidance for the builder beyond the task title/notes already stored."}}}`)
}
func (executeTaskTool) Permission(Context, json.RawMessage) PermissionRequest {
	return PermissionRequest{Decision: PermissionAllow}
}
func (executeTaskTool) Run(Context, json.RawMessage) (Result, error) {
	return Result{Title: "execute_task", Summary: "dispatched via runtime"}, nil
}
