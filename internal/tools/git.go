package tools

import (
	"encoding/json"
	"os/exec"
)

type gitStatusTool struct{}

func (gitStatusTool) Name() string            { return "git_status" }
func (gitStatusTool) Description() string     { return "Show git status for the workspace." }
func (gitStatusTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (gitStatusTool) Permission(Context, json.RawMessage) PermissionRequest {
	return PermissionRequest{Decision: PermissionAllow}
}
func (gitStatusTool) Run(ctx Context, input json.RawMessage) (Result, error) {
	out, err := exec.CommandContext(ctx.Context, "git", "-C", ctx.CWD, "status", "--short").CombinedOutput()
	return Result{
		Title:   "Git status",
		Summary: "git status --short",
		Content: []ContentBlock{{Type: "text", Text: string(out)}},
	}, err
}

type gitDiffTool struct{}

func (gitDiffTool) Name() string            { return "git_diff" }
func (gitDiffTool) Description() string     { return "Show git diff for the workspace." }
func (gitDiffTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (gitDiffTool) Permission(Context, json.RawMessage) PermissionRequest {
	return PermissionRequest{Decision: PermissionAllow}
}
func (gitDiffTool) Run(ctx Context, input json.RawMessage) (Result, error) {
	out, err := exec.CommandContext(ctx.Context, "git", "-C", ctx.CWD, "diff").CombinedOutput()
	return Result{
		Title:   "Git diff",
		Summary: "git diff",
		Content: []ContentBlock{{Type: "text", Text: string(out)}},
	}, err
}
