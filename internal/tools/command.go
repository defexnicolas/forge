package tools

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

type runCommandTool struct{}

func (runCommandTool) Name() string { return "run_command" }
func (runCommandTool) Description() string {
	return "Run a workspace command after permission checks. Commands stay inside the repo; optional managed Python venv lives under .forge/venv."
}
func (runCommandTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","required":["command"],"properties":{"command":{"type":"string","description":"Command to execute inside the workspace shell."},"shell":{"type":"string","description":"Optional shell override."},"cwd":{"type":"string","description":"Optional workspace-relative working directory."},"use_managed_venv":{"type":"boolean","description":"When true, create/reuse .forge/venv and prepend it to PATH before running the command."}}}`)
}
func (runCommandTool) Permission(Context, json.RawMessage) PermissionRequest {
	return PermissionRequest{Decision: PermissionAsk, Reason: "commands can change files or access the network"}
}
func (runCommandTool) Run(ctx Context, input json.RawMessage) (Result, error) {
	var req commandRequest
	if err := json.Unmarshal(input, &req); err != nil {
		return Result{}, err
	}
	workdir, err := resolveCommandWorkDir(ctx.CWD, req.CWD)
	if err != nil {
		return Result{}, err
	}
	shell, flag := defaultShell(req.Shell)
	cmd := exec.CommandContext(ctx.Context, shell, flag, req.Command)
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

	// A non-zero exit is the model's output to interpret, not a runtime
	// tool failure. `npm test` with a failing test, `grep` with no match,
	// `git diff` with pending changes — they all return non-zero and the
	// model needs to see the result. Surface the exit code in the content
	// but DO NOT propagate as a Go error: the runtime's loop guard halts
	// the session after two consecutive tool failures, and "exit status 1"
	// from a perfectly working `cmd.Run` was tripping it on every other
	// real-world session.
	//
	// Genuine tool failures (binary missing, exec setup error, ctx
	// cancellation) still propagate. The check order matters: ctx errors
	// can manifest as ExitError when the spawned process is killed by the
	// cancellation, so we look at ctx.Err first.
	if ctxErr := ctx.Context.Err(); ctxErr != nil {
		return Result{
			Title:   "Run command",
			Summary: req.Command,
			Content: []ContentBlock{{Type: "text", Text: text}},
		}, ctxErr
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			suffix := fmt.Sprintf("[exit %d]", exitErr.ExitCode())
			if text == "" {
				text = suffix
			} else {
				text = strings.TrimRight(text, "\n") + "\n" + suffix
			}
			err = nil
		}
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
