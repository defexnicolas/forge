package tools

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"runtime"
)

type runCommandTool struct{}

func (runCommandTool) Name() string        { return "run_command" }
func (runCommandTool) Description() string { return "Run a workspace command after permission checks." }
func (runCommandTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","required":["command"],"properties":{"command":{"type":"string"},"shell":{"type":"string"}}}`)
}
func (runCommandTool) Permission(Context, json.RawMessage) PermissionRequest {
	return PermissionRequest{Decision: PermissionAsk, Reason: "commands can change files or access the network"}
}
func (runCommandTool) Run(ctx Context, input json.RawMessage) (Result, error) {
	var req struct {
		Command string `json:"command"`
		Shell   string `json:"shell"`
	}
	if err := json.Unmarshal(input, &req); err != nil {
		return Result{}, err
	}
	shell, flag := defaultShell(req.Shell)
	cmd := exec.CommandContext(ctx.Context, shell, flag, req.Command)
	cmd.Dir = ctx.CWD
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	text := stdout.String()
	if stderr.Len() > 0 {
		text += "\n" + stderr.String()
	}
	return Result{
		Title:   "Run command",
		Summary: req.Command,
		Content: []ContentBlock{{Type: "text", Text: text}},
	}, err
}

func defaultShell(requested string) (string, string) {
	if requested != "" {
		if requested == "powershell" || requested == "pwsh" {
			return requested, "-Command"
		}
		return requested, "-c"
	}
	if runtime.GOOS == "windows" {
		return "powershell", "-Command"
	}
	return "sh", "-c"
}
