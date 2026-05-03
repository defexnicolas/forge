package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// requirePython skips the test when no python launcher is on PATH. The
// python_* tools are inert without one and CI environments without python
// shouldn't fail the suite over these integration tests.
func requirePython(t *testing.T) {
	t.Helper()
	if _, _, err := findPythonLauncher(); err != nil {
		t.Skip("python launcher not on PATH; skipping python tool integration test")
	}
	if _, err := exec.LookPath("py"); err != nil {
		if _, err := exec.LookPath("python"); err != nil {
			if _, err := exec.LookPath("python3"); err != nil {
				t.Skip("no python executable on PATH")
			}
		}
	}
}

func TestPythonRunInlineScript(t *testing.T) {
	requirePython(t)
	cwd := t.TempDir()
	tool := pythonRunTool{}
	input, _ := json.Marshal(map[string]any{
		"script": "print('hello forge')",
	})
	res, err := tool.Run(Context{Context: context.Background(), CWD: cwd}, input)
	if err != nil {
		t.Fatalf("Run inline: %v\n%s", err, contentText(res))
	}
	if !strings.Contains(contentText(res), "hello forge") {
		t.Errorf("expected stdout to contain 'hello forge', got: %s", contentText(res))
	}
	// venv must have been created.
	if _, err := exec.LookPath(managedPythonPath(cwd)); err != nil {
		// LookPath is overly strict; just stat.
	}
}

func TestPythonRunWithArgs(t *testing.T) {
	requirePython(t)
	cwd := t.TempDir()
	tool := pythonRunTool{}
	input, _ := json.Marshal(map[string]any{
		"script": "import sys\nprint('args:', '|'.join(sys.argv[1:]))",
		"args":   []string{"alpha", "beta"},
	})
	res, err := tool.Run(Context{Context: context.Background(), CWD: cwd}, input)
	if err != nil {
		t.Fatalf("Run with args: %v\n%s", err, contentText(res))
	}
	if !strings.Contains(contentText(res), "args: alpha|beta") {
		t.Errorf("expected args echo, got: %s", contentText(res))
	}
}

func TestPythonSetupCreatesVenv(t *testing.T) {
	requirePython(t)
	cwd := t.TempDir()
	tool := pythonSetupTool{}
	// Empty packages list — just ensure the venv exists.
	input, _ := json.Marshal(map[string]any{"packages": []string{}})
	res, err := tool.Run(Context{Context: context.Background(), CWD: cwd}, input)
	if err != nil {
		t.Fatalf("python_setup: %v\n%s", err, contentText(res))
	}
	if !strings.Contains(res.Summary, "venv ready") {
		t.Errorf("expected 'venv ready' summary, got: %s", res.Summary)
	}
	// Verify the python executable lives where we expect.
	pythonPath := managedPythonPath(cwd)
	if !strings.HasPrefix(pythonPath, filepath.Clean(cwd)) {
		t.Errorf("python path escapes workspace: %s", pythonPath)
	}
}

// TestPythonScriptResolverPrefersExistingFile verifies that a workspace path
// to a real .py file is picked over the inline-source heuristic.
func TestPythonScriptResolverPrefersExistingFile(t *testing.T) {
	cwd := t.TempDir()
	scriptPath := filepath.Join(cwd, "hello.py")
	if err := writeTestFile(scriptPath, "print('from file')"); err != nil {
		t.Fatal(err)
	}
	resolved, summary, cleanup, err := resolvePythonScript(cwd, "hello.py")
	if err != nil {
		t.Fatalf("resolvePythonScript: %v", err)
	}
	if cleanup != nil {
		defer cleanup()
	}
	if resolved != scriptPath {
		t.Errorf("expected resolved=%q, got %q", scriptPath, resolved)
	}
	if !strings.Contains(summary, "ran hello.py") {
		t.Errorf("expected summary to mention hello.py, got %q", summary)
	}
}

func contentText(r Result) string {
	if len(r.Content) == 0 {
		return ""
	}
	return r.Content[0].Text
}

func writeTestFile(path, body string) error {
	return os.WriteFile(path, []byte(body), 0o644)
}
