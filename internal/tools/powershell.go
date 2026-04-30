package tools

import (
	"bytes"
	"encoding/json"
	"os/exec"
)

type powershellTool struct{}

func (powershellTool) Name() string        { return "powershell_command" }
func (powershellTool) Description() string { return "Run a PowerShell command on Windows." }
func (powershellTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","required":["command"],"properties":{"command":{"type":"string","description":"The PowerShell command to execute."},"cwd":{"type":"string","description":"Optional workspace-relative working directory."},"use_managed_venv":{"type":"boolean","description":"When true, create/reuse .forge/venv and prepend it to PATH before running the command."}}}`)
}
func (powershellTool) Permission(Context, json.RawMessage) PermissionRequest {
	return PermissionRequest{Decision: PermissionAsk, Reason: "PowerShell commands can change files or access the network"}
}
func (powershellTool) Run(ctx Context, input json.RawMessage) (Result, error) {
	var req commandRequest
	if err := json.Unmarshal(input, &req); err != nil {
		return Result{}, err
	}
	workdir, err := resolveCommandWorkDir(ctx.CWD, req.CWD)
	if err != nil {
		return Result{}, err
	}
	// Try pwsh first (cross-platform PowerShell), fall back to powershell.
	shell := "pwsh"
	if _, err := exec.LookPath("pwsh"); err != nil {
		shell = "powershell"
	}
	cmd := exec.CommandContext(ctx.Context, shell, "-NoProfile", "-Command", req.Command)
	cmd.Dir = workdir
	cmd.Env, err = commandEnv(ctx.Context, ctx.CWD, req.UseManagedVenv)
	if err != nil {
		return Result{}, err
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	text := stdout.String()
	if stderr.Len() > 0 {
		text += "\n" + stderr.String()
	}
	return Result{
		Title:   "PowerShell",
		Summary: req.Command,
		Content: []ContentBlock{{Type: "text", Text: text}},
	}, err
}
