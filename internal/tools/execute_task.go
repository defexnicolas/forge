package tools

import "encoding/json"

// executeTaskTool is a shell. The real dispatch lives in
// internal/agent/runtime_exec.go (executeTaskTool method). It delegates the
// task to the "builder" subagent with only the task text and relevant_files
// as context; the builder never sees the full plan document.
type executeTaskTool struct{}

func (executeTaskTool) Name() string { return "execute_task" }
func (executeTaskTool) Description() string {
	return "Delegate ONE checklist task to the builder subagent. Pass only the minimal context the builder needs: the task_id and the list of files likely to be read or edited. Optional target_file/file_strategy/section_goal hints let the builder choose between one-shot artifact delivery and section-by-section execution for large files. The builder runs with its own model role (editor) and respects user approval for every mutating action."
}
func (executeTaskTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","required":["task_id"],"properties":{"task_id":{"type":"string","description":"ID of the task to execute, e.g. plan-1"},"relevant_files":{"type":"array","items":{"type":"string"},"description":"Minimal list of repo paths the builder should be aware of. Keep it tight; the builder can call read_file/list_files itself if it needs more."},"notes":{"type":"string","description":"Optional extra guidance for the builder beyond the task title/notes already stored."},"target_file":{"type":"string","description":"Optional primary file this task is modifying, used to steer artifact strategy."},"file_strategy":{"type":"string","description":"Optional execution hint such as one_shot_artifact for small runnable files or scaffold_then_patch for large new files."},"section_goal":{"type":"string","description":"Optional section this task owns, e.g. scaffold, head_metadata, content_sections, styling_hooks."}}}`)
}
func (executeTaskTool) Permission(Context, json.RawMessage) PermissionRequest {
	return PermissionRequest{Decision: PermissionAllow}
}
func (executeTaskTool) Run(Context, json.RawMessage) (Result, error) {
	return Result{Title: "execute_task", Summary: "dispatched via runtime"}, nil
}
