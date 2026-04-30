package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const managedVenvDirName = ".forge/venv"

type commandRequest struct {
	Command        string `json:"command"`
	Shell          string `json:"shell,omitempty"`
	CWD            string `json:"cwd,omitempty"`
	UseManagedVenv bool   `json:"use_managed_venv,omitempty"`
}

func resolveCommandWorkDir(root, requested string) (string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(requested) == "" {
		return rootAbs, nil
	}
	target := requested
	if !filepath.IsAbs(target) {
		target = filepath.Join(rootAbs, target)
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("command cwd escapes workspace: %s", requested)
	}
	return targetAbs, nil
}

func managedVenvDir(root string) string {
	return filepath.Join(root, filepath.FromSlash(managedVenvDirName))
}

func managedVenvBinDir(root string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(managedVenvDir(root), "Scripts")
	}
	return filepath.Join(managedVenvDir(root), "bin")
}

func managedPythonPath(root string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(managedVenvBinDir(root), "python.exe")
	}
	return filepath.Join(managedVenvBinDir(root), "python")
}

func ensureManagedPythonVenv(ctx context.Context, root string) (string, error) {
	pythonPath := managedPythonPath(root)
	if _, err := os.Stat(pythonPath); err == nil {
		return pythonPath, nil
	}
	if err := os.MkdirAll(filepath.Dir(managedVenvDir(root)), 0o755); err != nil {
		return "", err
	}
	exe, args, err := findPythonLauncher()
	if err != nil {
		return "", err
	}
	cmdArgs := append(append([]string{}, args...), "-m", "venv", managedVenvDir(root))
	cmd := exec.CommandContext(ctx, exe, cmdArgs...)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("create managed venv: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if _, err := os.Stat(pythonPath); err != nil {
		return "", fmt.Errorf("managed venv missing python executable at %s", pythonPath)
	}
	return pythonPath, nil
}

func findPythonLauncher() (string, []string, error) {
	candidates := []struct {
		exe  string
		args []string
	}{
		{exe: "python"},
		{exe: "python3"},
	}
	if runtime.GOOS == "windows" {
		candidates = append([]struct {
			exe  string
			args []string
		}{
			{exe: "py", args: []string{"-3"}},
		}, candidates...)
	}
	for _, candidate := range candidates {
		if _, err := exec.LookPath(candidate.exe); err == nil {
			return candidate.exe, candidate.args, nil
		}
	}
	return "", nil, fmt.Errorf("python launcher not found; install Python or add it to PATH")
}

func commandEnv(ctx context.Context, root string, useManagedVenv bool) ([]string, error) {
	env := append([]string{}, os.Environ()...)
	if !useManagedVenv {
		return env, nil
	}
	if _, err := ensureManagedPythonVenv(ctx, root); err != nil {
		return nil, err
	}
	binDir := managedVenvBinDir(root)
	pathKey := "PATH"
	pathValue := ""
	for _, entry := range env {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], pathKey) {
			pathValue = parts[1]
			break
		}
	}
	updated := []string{
		"VIRTUAL_ENV=" + managedVenvDir(root),
		pathKey + "=" + binDir + string(os.PathListSeparator) + pathValue,
	}
	filtered := env[:0]
	for _, entry := range env {
		lower := strings.ToLower(entry)
		if strings.HasPrefix(lower, "virtual_env=") || strings.HasPrefix(lower, "path=") {
			continue
		}
		filtered = append(filtered, entry)
	}
	filtered = append(filtered, updated...)
	return filtered, nil
}
