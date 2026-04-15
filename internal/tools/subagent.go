package tools

import "encoding/json"

type spawnSubagentTool struct{}

func (spawnSubagentTool) Name() string { return "spawn_subagent" }
func (spawnSubagentTool) Description() string {
	return "Spawn one limited subagent worker. The subagent uses its configured model role and a reduced task context."
}
func (spawnSubagentTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"agent":{"type":"string","description":"Subagent name such as explorer, reviewer, tester, summarizer, debug, docs, refactorer, or commit"},"prompt":{"type":"string","description":"Specific task for the subagent"},"input":{"type":"string","description":"Alias for prompt"},"context":{"description":"Optional structured context for the subagent"}}}`)
}
func (spawnSubagentTool) Permission(Context, json.RawMessage) PermissionRequest {
	return PermissionRequest{Decision: PermissionAllow}
}
func (spawnSubagentTool) Run(Context, json.RawMessage) (Result, error) {
	return Result{Title: "spawn_subagent", Summary: "dispatched via runtime"}, nil
}

type spawnSubagentsTool struct{}

func (spawnSubagentsTool) Name() string { return "spawn_subagents" }
func (spawnSubagentsTool) Description() string {
	return "Spawn multiple independent read-only/analysis subagent tasks concurrently. Results are returned in input order."
}
func (spawnSubagentsTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","required":["tasks"],"properties":{"tasks":{"type":"array","items":{"type":"object","properties":{"agent":{"type":"string","description":"Subagent name such as explorer, reviewer, tester, summarizer, or debug"},"prompt":{"type":"string","description":"Specific independent task for this subagent"},"input":{"type":"string","description":"Alias for prompt"},"context":{"description":"Optional structured context for this subagent"}}}},"max_concurrency":{"type":"integer","minimum":1,"maximum":8,"description":"Maximum concurrent subagents. Default 3, capped at 8."}}}`)
}
func (spawnSubagentsTool) Permission(Context, json.RawMessage) PermissionRequest {
	return PermissionRequest{Decision: PermissionAllow}
}
func (spawnSubagentsTool) Run(Context, json.RawMessage) (Result, error) {
	return Result{Title: "spawn_subagents", Summary: "dispatched via runtime"}, nil
}
