package tools

import (
	"encoding/json"
	"fmt"
)

type askUserTool struct{}

func (askUserTool) Name() string { return "ask_user" }
func (askUserTool) Description() string {
	return "Ask the user a single focused question and wait for their response. " +
		"Optionally supply up to 3 short suggested answers via `options`; the TUI will render them as picks and add a 'Write my own' row. " +
		"Good suggestions are concrete and mutually exclusive (e.g. \"Yes\"/\"No\"/\"Only for public endpoints\")."
}
func (askUserTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"required":["question"],
		"properties":{
			"question":{"type":"string","description":"The question to ask the user."},
			"options":{
				"type":"array",
				"items":{"type":"string"},
				"maxItems":3,
				"description":"Up to 3 short suggested answers. The TUI renders each as a selectable row and always adds a 'Write my own' row at the bottom."
			}
		}
	}`)
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
