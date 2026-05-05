package tools

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestResolveCommandWorkDirRejectsWorkspaceEscape(t *testing.T) {
	_, err := resolveCommandWorkDir(t.TempDir(), "..")
	if err == nil || !strings.Contains(err.Error(), "escapes workspace") {
		t.Fatalf("expected workspace escape error, got %v", err)
	}
}

func TestRunCommandToolRejectsWorkspaceEscape(t *testing.T) {
	tool := runCommandTool{}
	input := json.RawMessage(`{"command":"echo hi","cwd":".."}`)
	_, err := tool.Run(Context{Context: context.Background(), CWD: t.TempDir()}, input)
	if err == nil || !strings.Contains(err.Error(), "escapes workspace") {
		t.Fatalf("expected workspace escape error, got %v", err)
	}
}

// TestRunCommandToolNonZeroExitNotAToolFailure verifies that a command
// which ran successfully but exited non-zero (the common case for `npm
// test` with a failing test, `grep` with no match, `git diff` with
// pending changes, etc.) does NOT propagate as a Go error. The exit code
// is surfaced in the content so the model can interpret it; nil err
// means the runtime's loop-breaker guard won't count this toward
// "tool failed N times in a row".
func TestRunCommandToolNonZeroExitNotAToolFailure(t *testing.T) {
	tool := runCommandTool{}
	cwd := t.TempDir()
	// `exit 7` returns code 7 across both sh and PowerShell.
	input := json.RawMessage(`{"command":"exit 7"}`)
	result, err := tool.Run(Context{Context: context.Background(), CWD: cwd}, input)
	if err != nil {
		t.Fatalf("non-zero exit should not propagate as Go error, got %v", err)
	}
	if len(result.Content) == 0 {
		t.Fatalf("expected exit info in content, got empty")
	}
	if !strings.Contains(result.Content[0].Text, "[exit 7]") {
		t.Fatalf("expected '[exit 7]' marker, got %q", result.Content[0].Text)
	}
	if strings.Contains(strings.ToLower(result.Summary), "failed") {
		t.Errorf("summary must not be poisoned with 'failed' on non-zero exit, got %q", result.Summary)
	}
}

func TestRunCommandToolManagedVenvCreatesForgeVenv(t *testing.T) {
	if _, _, err := findPythonLauncher(); err != nil {
		t.Skip(err.Error())
	}
	cwd := t.TempDir()
	tool := runCommandTool{}
	input := json.RawMessage(`{"command":"python --version","use_managed_venv":true}`)
	result, err := tool.Run(Context{Context: context.Background(), CWD: cwd}, input)
	if err != nil {
		t.Fatalf("run_command with managed venv failed: %v\n%s", err, result.Content)
	}
	if _, err := os.Stat(managedPythonPath(cwd)); err != nil {
		t.Fatalf("managed venv python missing: %v", err)
	}
	if len(result.Content) == 0 || !strings.Contains(strings.ToLower(result.Content[0].Text), "python") {
		t.Fatalf("unexpected managed venv output: %#v", result.Content)
	}
}
