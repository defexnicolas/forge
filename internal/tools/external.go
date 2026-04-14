package tools

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

type externalToolConfig struct {
	Name        string            `toml:"name"`
	Description string            `toml:"description"`
	Runtime     string            `toml:"runtime"`
	Command     string            `toml:"command"`
	Permission  string            `toml:"permission"`
	Schema      map[string]any    `toml:"schema"`
	Env         map[string]string `toml:"env"`
}

type externalTool struct {
	root   string
	config externalToolConfig
	schema json.RawMessage
}

func RegisterExternal(registry *Registry, cwd string) error {
	root := filepath.Join(cwd, ".forge", "tools")
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		toolRoot := filepath.Join(root, entry.Name())
		tool, err := loadExternalTool(toolRoot)
		if err != nil {
			return err
		}
		registry.Register(tool)
	}
	return nil
}

func loadExternalTool(root string) (Tool, error) {
	data, err := os.ReadFile(filepath.Join(root, "tool.toml"))
	if err != nil {
		return nil, err
	}
	var cfg externalToolConfig
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if cfg.Name == "" {
		return nil, fmt.Errorf("%s: missing tool name", root)
	}
	if cfg.Runtime == "" {
		cfg.Runtime = "process"
	}
	if cfg.Permission == "" {
		cfg.Permission = "ask"
	}
	schema, err := json.Marshal(cfg.Schema)
	if err != nil {
		return nil, err
	}
	if len(schema) == 0 || string(schema) == "null" {
		schema = []byte(`{"type":"object"}`)
	}
	return externalTool{root: root, config: cfg, schema: schema}, nil
}

func (t externalTool) Name() string            { return t.config.Name }
func (t externalTool) Description() string     { return t.config.Description }
func (t externalTool) Schema() json.RawMessage { return t.schema }
func (t externalTool) Permission(Context, json.RawMessage) PermissionRequest {
	switch PermissionDecision(t.config.Permission) {
	case PermissionAllow:
		return PermissionRequest{Decision: PermissionAllow}
	case PermissionDeny:
		return PermissionRequest{Decision: PermissionDeny, Reason: "tool config denies execution"}
	default:
		return PermissionRequest{Decision: PermissionAsk, Reason: "external tools require approval by default"}
	}
}

func (t externalTool) Run(ctx Context, input json.RawMessage) (Result, error) {
	if t.config.Command == "" {
		return Result{}, fmt.Errorf("external tool %s has no command", t.config.Name)
	}
	request := map[string]any{
		"tool":  t.config.Name,
		"input": json.RawMessage(input),
		"context": map[string]string{
			"cwd":       ctx.CWD,
			"sessionId": ctx.SessionID,
			"agent":     ctx.Agent,
		},
	}
	body, err := json.Marshal(request)
	if err != nil {
		return Result{}, err
	}

	shell, flag := externalShell()
	cmd := exec.CommandContext(ctx.Context, shell, flag, t.config.Command)
	cmd.Dir = t.root
	cmd.Stdin = bytes.NewReader(body)
	cmd.Env = t.env()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return Result{}, fmt.Errorf("%s failed: %w\n%s", t.config.Name, err, stderr.String())
	}
	var result Result
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return Result{
			Title:   t.config.Name,
			Summary: "External tool returned plain text.",
			Content: []ContentBlock{{Type: "text", Text: stdout.String()}},
		}, nil
	}
	return result, nil
}

func (t externalTool) env() []string {
	env := os.Environ()
	for key, value := range t.config.Env {
		env = append(env, key+"="+expandEnv(value))
	}
	env = append(env, "FORGE_TOOL_ROOT="+t.root)
	return env
}

func expandEnv(value string) string {
	return os.Expand(value, func(key string) string {
		return os.Getenv(strings.TrimSpace(key))
	})
}

func externalShell() (string, string) {
	if runtime.GOOS == "windows" {
		return "powershell", "-Command"
	}
	return "sh", "-c"
}
