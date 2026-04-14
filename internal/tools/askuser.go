package tools

import (
	"encoding/json"
	"fmt"
)

type askUserTool struct{}

func (askUserTool) Name() string        { return "ask_user" }
func (askUserTool) Description() string { return "Ask the user a question and wait for their response." }
func (askUserTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","required":["question"],"properties":{"question":{"type":"string","description":"The question to ask the user."}}}`)
}
func (askUserTool) Permission(Context, json.RawMessage) PermissionRequest {
	return PermissionRequest{Decision: PermissionAllow}
}
func (askUserTool) Run(ctx Context, input json.RawMessage) (Result, error) {
	var req struct {
		Question string `json:"question"`
	}
	if err := json.Unmarshal(input, &req); err != nil {
		return Result{}, err
	}
	if req.Question == "" {
		return Result{}, fmt.Errorf("question is required")
	}
	// The actual user interaction is handled by the agent runtime,
	// which intercepts this tool call and shows a prompt in the TUI.
	// This Run method is a fallback that returns the question as output.
	return Result{
		Title:   "Ask user",
		Summary: req.Question,
		Content: []ContentBlock{{Type: "text", Text: "Question: " + req.Question}},
	}, nil
}
