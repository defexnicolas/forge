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
