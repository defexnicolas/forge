package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type readFileTool struct{}

func (readFileTool) Name() string        { return "read_file" }
func (readFileTool) Description() string { return "Read a text file from the workspace." }
func (readFileTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","required":["path"],"properties":{"path":{"type":"string"}}}`)
}
func (readFileTool) Permission(Context, json.RawMessage) PermissionRequest {
	return PermissionRequest{Decision: PermissionAllow}
}
func (readFileTool) Run(ctx Context, input json.RawMessage) (Result, error) {
	var req struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(input, &req); err != nil {
		return Result{}, err
	}
	path, err := workspacePath(ctx.CWD, req.Path)
	if err != nil {
		return Result{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Result{}, err
	}
	return Result{
		Title:   "Read file",
		Summary: req.Path,
		Content: []ContentBlock{{Type: "text", Text: string(data), Path: path}},
	}, nil
}

type listFilesTool struct{}

func (listFilesTool) Name() string        { return "list_files" }
func (listFilesTool) Description() string { return "List files in a workspace directory." }
func (listFilesTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`)
}
func (listFilesTool) Permission(Context, json.RawMessage) PermissionRequest {
	return PermissionRequest{Decision: PermissionAllow}
}
func (listFilesTool) Run(ctx Context, input json.RawMessage) (Result, error) {
	var req struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(input, &req); err != nil {
		return Result{}, err
	}
	path, err := workspacePath(ctx.CWD, req.Path)
	if err != nil {
		return Result{}, err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return Result{}, err
	}
	lines := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			name += "/"
		}
		lines = append(lines, name)
	}
	return Result{
		Title:   "List files",
		Summary: fmt.Sprintf("%d entries in %s", len(lines), path),
		Content: []ContentBlock{{Type: "text", Text: strings.Join(lines, "\n"), Path: path}},
	}, nil
}

type searchFilesTool struct{}

func (searchFilesTool) Name() string        { return "search_files" }
func (searchFilesTool) Description() string { return "Find files by a simple substring pattern." }
func (searchFilesTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","required":["pattern"],"properties":{"pattern":{"type":"string"}}}`)
}
func (searchFilesTool) Permission(Context, json.RawMessage) PermissionRequest {
	return PermissionRequest{Decision: PermissionAllow}
}
func (searchFilesTool) Run(ctx Context, input json.RawMessage) (Result, error) {
	var req struct {
		Pattern string `json:"pattern"`
	}
	if err := json.Unmarshal(input, &req); err != nil {
		return Result{}, err
	}
	matches, err := runSearchFiles(context.Background(), ctx.CWD, req.Pattern)
	if err != nil {
		return Result{}, err
	}
	backend := "ripgrep"
	if ripgrepPath() == "" {
		backend = "go-fallback"
	}
	return Result{
		Title:   "Search files",
		Summary: fmt.Sprintf("%d matches (%s)", len(matches), backend),
		Content: []ContentBlock{{Type: "text", Text: strings.Join(matches, "\n")}},
	}, nil
}

func workspacePath(cwd, rel string) (string, error) {
	if rel == "" {
		rel = "."
	}
	path := rel
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, rel)
	}
	cleanCWD, err := filepath.Abs(cwd)
	if err != nil {
		return "", err
	}
	cleanPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if cleanPath != cleanCWD && !strings.HasPrefix(cleanPath, cleanCWD+string(os.PathSeparator)) {
		return "", fmt.Errorf("path escapes workspace: %s", rel)
	}
	return cleanPath, nil
}
