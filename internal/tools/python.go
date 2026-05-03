package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// pythonSetupTool prepares the workspace's managed Python venv (.forge/venv)
// and installs requested PyPI packages into it. Idempotent: an existing venv
// is reused, and pip itself decides whether each package needs work.
//
// PermissionAllow because the side effects are bounded: writes only happen
// inside the workspace's .forge/ directory and the package source is the
// already-trusted Python ecosystem. Compare to run_command which can do
// anything anywhere — separating this lets the model install the deps a
// task needs without nagging the user for approval on every pip call.
type pythonSetupTool struct{}

func (pythonSetupTool) Name() string { return "python_setup" }
func (pythonSetupTool) Description() string {
	return "Create or reuse the workspace's .forge/venv and install Python packages into it. Use this once before python_run when a script needs PyPI deps (e.g. {\"packages\":[\"reportlab\"]} to enable PDF generation)."
}
func (pythonSetupTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"packages":{"type":"array","items":{"type":"string"},"description":"PyPI package specs to install (e.g. [\"reportlab\", \"Pillow>=10\"]). Empty array still ensures the venv exists."},"requirements_file":{"type":"string","description":"Optional workspace-relative path to a requirements.txt to install."}}}`)
}
func (pythonSetupTool) Permission(Context, json.RawMessage) PermissionRequest {
	return PermissionRequest{Decision: PermissionAllow}
}
func (pythonSetupTool) Run(ctx Context, input json.RawMessage) (Result, error) {
	var req struct {
		Packages         []string `json:"packages"`
		RequirementsFile string   `json:"requirements_file"`
	}
	if err := json.Unmarshal(input, &req); err != nil {
		return Result{}, err
	}
	pythonPath, err := ensureManagedPythonVenv(ctx.Context, ctx.CWD)
	if err != nil {
		return Result{}, err
	}

	var steps []pythonStep

	run := func(title string, args ...string) bool {
		cmd := exec.CommandContext(ctx.Context, pythonPath, args...)
		cmd.Dir = ctx.CWD
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		out := stdout.String()
		if stderr.Len() > 0 {
			out += "\n" + stderr.String()
		}
		steps = append(steps, pythonStep{title: title, stdout: out, err: err})
		return err == nil
	}

	if len(req.Packages) > 0 {
		args := append([]string{"-m", "pip", "install", "--disable-pip-version-check"}, req.Packages...)
		if !run("pip install "+strings.Join(req.Packages, " "), args...) {
			return summarizeSteps("python_setup", steps), steps[len(steps)-1].err
		}
	}
	if reqFile := strings.TrimSpace(req.RequirementsFile); reqFile != "" {
		path, perr := workspacePath(ctx.CWD, reqFile)
		if perr != nil {
			return Result{Title: "python_setup", Summary: perr.Error()}, perr
		}
		if _, statErr := os.Stat(path); statErr != nil {
			return Result{Title: "python_setup", Summary: "requirements_file not found: " + reqFile}, statErr
		}
		if !run("pip install -r "+reqFile, "-m", "pip", "install", "--disable-pip-version-check", "-r", path) {
			return summarizeSteps("python_setup", steps), steps[len(steps)-1].err
		}
	}
	if len(steps) == 0 {
		return Result{
			Title:   "python_setup",
			Summary: "venv ready at " + filepath.Dir(pythonPath),
			Content: []ContentBlock{{Type: "text", Text: "managed venv exists; nothing to install."}},
		}, nil
	}
	return summarizeSteps("python_setup", steps), nil
}

// pythonRunTool executes a Python script inside the managed venv. The script
// argument can be either a workspace-relative path to an existing .py file
// or an inline source string (forge writes it to a temp file under
// .forge/venv/_inline.py before running). Stdout + stderr are captured and
// returned in the result so the model can inspect output without an extra
// read_file call.
type pythonRunTool struct{}

func (pythonRunTool) Name() string { return "python_run" }
func (pythonRunTool) Description() string {
	return "Run a Python script in the workspace's managed venv. `script` is either a workspace path to a .py file or inline Python source. The venv is auto-created if missing; call python_setup first to install required packages."
}
func (pythonRunTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","required":["script"],"properties":{"script":{"type":"string","description":"Workspace-relative path to a .py file, OR inline Python source code."},"args":{"type":"array","items":{"type":"string"},"description":"Arguments passed to the script as sys.argv[1:]."},"stdin":{"type":"string","description":"Optional text piped to the script's stdin."}}}`)
}
func (pythonRunTool) Permission(Context, json.RawMessage) PermissionRequest {
	return PermissionRequest{Decision: PermissionAllow}
}
func (pythonRunTool) Run(ctx Context, input json.RawMessage) (Result, error) {
	var req struct {
		Script string   `json:"script"`
		Args   []string `json:"args"`
		Stdin  string   `json:"stdin"`
	}
	if err := json.Unmarshal(input, &req); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(req.Script) == "" {
		return Result{Title: "python_run", Summary: "script is required"}, fmt.Errorf("script is required")
	}
	pythonPath, err := ensureManagedPythonVenv(ctx.Context, ctx.CWD)
	if err != nil {
		return Result{}, err
	}

	scriptPath, summary, cleanup, err := resolvePythonScript(ctx.CWD, req.Script)
	if err != nil {
		return Result{Title: "python_run", Summary: err.Error()}, err
	}
	if cleanup != nil {
		defer cleanup()
	}

	args := append([]string{scriptPath}, req.Args...)
	cmd := exec.CommandContext(ctx.Context, pythonPath, args...)
	cmd.Dir = ctx.CWD
	if req.Stdin != "" {
		cmd.Stdin = strings.NewReader(req.Stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	out := stdout.String()
	if stderr.Len() > 0 {
		if out != "" {
			out += "\n--- stderr ---\n"
		}
		out += stderr.String()
	}
	return Result{
		Title:   "python_run",
		Summary: summary,
		Content: []ContentBlock{{Type: "text", Text: out}},
	}, runErr
}

// resolvePythonScript decides whether the input is a path or inline source.
// A path is anything that exists on disk under cwd. Anything else is treated
// as inline code and written to .forge/venv/_inline.py so tracebacks point
// at a real file. Returns the path to invoke, a human-readable summary, and
// an optional cleanup func for the inline temp file.
func resolvePythonScript(cwd, script string) (string, string, func(), error) {
	trimmed := strings.TrimSpace(script)
	// Heuristic: if it starts with a newline or contains 'import '/'def '/'print('
	// at the top, it's inline. Otherwise try to resolve as a path first.
	looksInline := strings.Contains(trimmed, "\n") ||
		strings.HasPrefix(trimmed, "import ") ||
		strings.HasPrefix(trimmed, "from ") ||
		strings.HasPrefix(trimmed, "print(") ||
		strings.HasPrefix(trimmed, "def ")
	if !looksInline {
		path, err := workspacePath(cwd, script)
		if err == nil {
			if _, statErr := os.Stat(path); statErr == nil {
				return path, "ran " + script, nil, nil
			}
		}
	}
	// Treat as inline source.
	venvDir := managedVenvDir(cwd)
	if err := os.MkdirAll(venvDir, 0o755); err != nil {
		return "", "", nil, err
	}
	tmpPath := filepath.Join(venvDir, "_inline.py")
	if err := os.WriteFile(tmpPath, []byte(script), 0o644); err != nil {
		return "", "", nil, err
	}
	cleanup := func() { _ = os.Remove(tmpPath) }
	preview := strings.SplitN(trimmed, "\n", 2)[0]
	if len(preview) > 60 {
		preview = preview[:57] + "..."
	}
	return tmpPath, "ran inline: " + preview, cleanup, nil
}

type pythonStep struct {
	title  string
	stdout string
	err    error
}

func summarizeSteps(title string, steps []pythonStep) Result {
	var b strings.Builder
	for i, s := range steps {
		if i > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "$ %s\n%s", s.title, strings.TrimSpace(s.stdout))
		if s.err != nil {
			fmt.Fprintf(&b, "\n[error] %s", s.err.Error())
		}
	}
	last := steps[len(steps)-1]
	summary := last.title
	if last.err != nil {
		summary = "failed: " + last.title
	}
	return Result{
		Title:   title,
		Summary: summary,
		Content: []ContentBlock{{Type: "text", Text: b.String()}},
	}
}
