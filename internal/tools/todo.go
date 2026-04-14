package tools

import (
	"encoding/json"
	"strings"
)

type todoWriteTool struct{}

func (todoWriteTool) Name() string        { return "todo_write" }
func (todoWriteTool) Description() string { return "Update the visible agent plan." }
func (todoWriteTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","required":["items"],"properties":{"items":{"type":"array","items":{"type":"string"}}}}`)
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
	return Result{
		Title:   "Todo plan",
		Summary: "Updated plan",
		Content: []ContentBlock{{Type: "text", Text: strings.Join(req.Items, "\n")}},
	}, nil
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
