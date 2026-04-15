package tools

import "encoding/json"

type planWriteTool struct{}

func (planWriteTool) Name() string { return "plan_write" }
func (planWriteTool) Description() string {
	return "Save the full planning document. Use this for design intent, assumptions, approach, stubs, risks, and validation; keep task status in task_* or todo_write."
}
func (planWriteTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"summary":{"type":"string","description":"Short statement of the goal"},"context":{"type":"string","description":"Relevant repository facts and constraints"},"assumptions":{"type":"array","items":{"type":"string"},"description":"Assumptions to verify or preserve"},"approach":{"type":"string","description":"Detailed implementation strategy"},"stubs":{"type":"array","items":{"type":"string"},"description":"Potential files, functions, structs, commands, or pseudocode stubs to create or modify"},"risks":{"type":"array","items":{"type":"string"},"description":"Known risks, edge cases, or open questions"},"validation":{"type":"array","items":{"type":"string"},"description":"Tests, commands, and manual checks to run"}}}`)
}
func (planWriteTool) Permission(Context, json.RawMessage) PermissionRequest {
	return PermissionRequest{Decision: PermissionAllow}
}
func (planWriteTool) Run(Context, json.RawMessage) (Result, error) {
	return Result{Title: "plan_write", Summary: "dispatched via runtime"}, nil
}

type planGetTool struct{}

func (planGetTool) Name() string { return "plan_get" }
func (planGetTool) Description() string {
	return "Read the current full planning document. Use this when the user asks to inspect or refine the plan."
}
func (planGetTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (planGetTool) Permission(Context, json.RawMessage) PermissionRequest {
	return PermissionRequest{Decision: PermissionAllow}
}
func (planGetTool) Run(Context, json.RawMessage) (Result, error) {
	return Result{Title: "plan_get", Summary: "dispatched via runtime"}, nil
}
