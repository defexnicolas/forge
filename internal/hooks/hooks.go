package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Hook represents a single hook definition loaded from hooks.json.
type Hook struct {
	Event   string `json:"event"`
	Match   string `json:"match,omitempty"`
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"` // seconds, default 30
}

// Runner loads and executes hooks for forge events.
type Runner struct {
	cwd   string
	hooks []Hook
}

type hooksFile struct {
	Hooks []Hook `json:"hooks"`
}

// NewRunner creates a Runner and auto-loads hooks from .forge/hooks.json.
func NewRunner(cwd string) *Runner {
	r := &Runner{cwd: cwd}
	path := filepath.Join(cwd, ".forge", "hooks.json")
	_ = r.Load(path) // missing file is not an error
	return r
}

// Load parses a hooks.json file and appends hooks to the runner.
func (r *Runner) Load(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var file hooksFile
	if err := json.Unmarshal(data, &file); err != nil {
		return fmt.Errorf("hooks: parse %s: %w", path, err)
	}
	for i := range file.Hooks {
		file.Hooks[i].Event = normalizeEvent(file.Hooks[i].Event)
	}
	r.hooks = append(r.hooks, file.Hooks...)
	return nil
}

// RunBefore runs all matching before-hooks synchronously.
// Returns an error if any hook exits with non-zero (blocking the action).
func (r *Runner) RunBefore(event, toolName string) error {
	for _, hook := range r.hooks {
		if !matchesHook(hook, event, toolName) {
			continue
		}
		if err := r.execute(hook, toolName, nil); err != nil {
			return fmt.Errorf("%s: %w", hook.Command, err)
		}
	}
	return nil
}

// RunAfter runs all matching after-hooks. Errors are silently ignored.
func (r *Runner) RunAfter(event, toolName string, changedFiles []string) {
	for _, hook := range r.hooks {
		if !matchesHook(hook, event, toolName) {
			continue
		}
		_ = r.execute(hook, toolName, changedFiles)
	}
}

// Describe returns a human-readable list of loaded hooks.
func (r *Runner) Describe() string {
	if len(r.hooks) == 0 {
		return "No hooks loaded. Add .forge/hooks.json to configure hooks."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d hook(s) loaded:\n", len(r.hooks))
	for _, hook := range r.hooks {
		match := "*"
		if hook.Match != "" {
			match = hook.Match
		}
		fmt.Fprintf(&b, "- %s [%s] %s\n", hook.Event, match, hook.Command)
	}
	return strings.TrimSpace(b.String())
}

func (r *Runner) execute(hook Hook, toolName string, changedFiles []string) error {
	timeout := time.Duration(hook.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := shellCommand(ctx, hook.Command)
	cmd.Dir = r.cwd
	cmd.Env = append(os.Environ(),
		"FORGE_CWD="+r.cwd,
		"FORGE_TOOL="+toolName,
		"FORGE_EVENT="+hook.Event,
		"FORGE_CHANGED_FILES="+strings.Join(changedFiles, " "),
	)
	cmd.Stdout = os.Stderr // hooks output goes to stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func shellCommand(ctx context.Context, command string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.CommandContext(ctx, "powershell", "-NoProfile", "-Command", command)
	}
	return exec.CommandContext(ctx, "sh", "-c", command)
}

func normalizeEvent(event string) string {
	switch event {
	case "PreToolUse":
		return "before:tool_call"
	case "PostToolUse":
		return "after:tool_call"
	case "UserPromptSubmit":
		return "session:prompt"
	case "SessionStart":
		return "session:start"
	case "SessionEnd":
		return "session:end"
	case "PreCompact":
		return "before:compact"
	default:
		return event
	}
}

func matchesHook(hook Hook, event, toolName string) bool {
	if hook.Event != event {
		return false
	}
	if hook.Match == "" {
		return true
	}
	if hook.Match == toolName {
		return true
	}
	// Support simple glob: "edit_*" matches "edit_file"
	matched, _ := filepath.Match(hook.Match, toolName)
	return matched
}
