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

func (readFileTool) Name() string { return "read_file" }
func (readFileTool) Description() string {
	return "Read a text file from the workspace. Optional offset (1-based start line) and limit (max lines) let you paginate large files. The runtime caps tool result size, so for files >150 lines call read_file again with offset+limit instead of trying to re-read the whole thing — re-reading without pagination just gives you the same head+tail truncation. The summary always includes 'lines X-Y of N' so you know whether more remains."
}
func (readFileTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","required":["path"],"properties":{"path":{"type":"string","description":"Workspace-relative path to the file."},"offset":{"type":"integer","minimum":1,"description":"1-based starting line. Omit or 0 to read from the start."},"limit":{"type":"integer","minimum":1,"description":"Maximum number of lines to return. Omit to read to the end of the file."}}}`)
}
func (readFileTool) Permission(Context, json.RawMessage) PermissionRequest {
	return PermissionRequest{Decision: PermissionAllow}
}
func (readFileTool) Run(ctx Context, input json.RawMessage) (Result, error) {
	var req struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
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
	text, summary := sliceFileLines(string(data), req.Path, req.Offset, req.Limit)
	return Result{
		Title:   "Read file",
		Summary: summary,
		Content: []ContentBlock{{Type: "text", Text: text, Path: path}},
	}, nil
}

// sliceFileLines applies an optional 1-based offset + limit to file
// contents and returns (sliced_text, summary_with_metadata). When the
// file is fully read, summary is just the path; otherwise it carries
// "path (lines A-B of N)" so the model knows whether more remains and
// can paginate by adjusting offset.
//
// offset <= 0 is treated as "from the start"; limit <= 0 means "to the
// end". Out-of-range offsets clamp to the valid range and return an
// empty slice rather than an error — the model can then ask for an
// earlier offset instead of the runtime halting.
func sliceFileLines(content, displayPath string, offset, limit int) (string, string) {
	if offset <= 0 && limit <= 0 {
		return content, displayPath
	}
	lines := strings.Split(content, "\n")
	total := len(lines)
	// strings.Split on a string ending in "\n" yields a trailing empty
	// element; treat it as not a real "line" for counting purposes so
	// the user-visible total matches what `wc -l` reports.
	if total > 0 && lines[total-1] == "" {
		total--
		lines = lines[:total]
	}
	start := offset - 1 // convert 1-based to 0-based
	if start < 0 {
		start = 0
	}
	if start > total {
		start = total
	}
	end := total
	if limit > 0 && start+limit < end {
		end = start + limit
	}
	sliced := strings.Join(lines[start:end], "\n")
	if start == 0 && end == total {
		return sliced, displayPath
	}
	return sliced, fmt.Sprintf("%s (lines %d-%d of %d)", displayPath, start+1, end, total)
}

// readFilesTool reads multiple files in a single tool call. Solves the
// "model fans out N read_file calls and then loops" pattern: the model
// can name every file it needs up front and get them all in one tool
// result, cutting N round-trips through the LLM down to one.
//
// Per-file offset/limit is intentionally not supported — when the model
// needs different ranges per file it can fall back to single read_file
// calls. The shared `offset` / `limit` here applies to every path
// (useful for "give me the first 100 lines of these 5 files").
type readFilesTool struct{}

func (readFilesTool) Name() string { return "read_files" }
func (readFilesTool) Description() string {
	return "Read multiple workspace files in a single call. Pass `paths` (array of relative paths). Optional `offset` and `limit` apply per file. Use this whenever you need to inspect 2+ files for a task — one batched call is much cheaper than N separate read_file invocations and avoids the back-and-forth that triggers narration loops. The result concatenates each file as a labelled section. Per-file size caps still apply, so for files >150 lines either narrow `limit` or fall back to single read_file calls with offset for the parts you need."
}
func (readFilesTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","required":["paths"],"properties":{"paths":{"type":"array","minItems":1,"items":{"type":"string"},"description":"Workspace-relative paths. 1 to 16 entries; if you need more, split into successive calls."},"offset":{"type":"integer","minimum":1,"description":"1-based starting line, applied to every file."},"limit":{"type":"integer","minimum":1,"description":"Max lines per file. Useful for skim-reading many files at once."}}}`)
}
func (readFilesTool) Permission(Context, json.RawMessage) PermissionRequest {
	return PermissionRequest{Decision: PermissionAllow}
}
func (readFilesTool) Run(ctx Context, input json.RawMessage) (Result, error) {
	var req struct {
		Paths  []string `json:"paths"`
		Offset int      `json:"offset"`
		Limit  int      `json:"limit"`
	}
	if err := json.Unmarshal(input, &req); err != nil {
		return Result{}, err
	}
	if len(req.Paths) == 0 {
		return Result{}, fmt.Errorf("paths is required and must not be empty")
	}
	const maxBatch = 16
	if len(req.Paths) > maxBatch {
		return Result{}, fmt.Errorf("too many paths: %d (max %d). Split into successive calls", len(req.Paths), maxBatch)
	}

	var b strings.Builder
	var firstAbs string
	successful := 0
	for i, rel := range req.Paths {
		if i > 0 {
			b.WriteString("\n\n")
		}
		path, err := workspacePath(ctx.CWD, rel)
		if err != nil {
			fmt.Fprintf(&b, "=== %s ===\nERROR: %s", rel, err.Error())
			continue
		}
		if firstAbs == "" {
			firstAbs = path
		}
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(&b, "=== %s ===\nERROR: %s", rel, err.Error())
			continue
		}
		text, header := sliceFileLines(string(data), rel, req.Offset, req.Limit)
		fmt.Fprintf(&b, "=== %s ===\n%s", header, text)
		successful++
	}

	summary := fmt.Sprintf("read %d/%d files", successful, len(req.Paths))
	return Result{
		Title:   "Read files",
		Summary: summary,
		Content: []ContentBlock{{Type: "text", Text: b.String(), Path: firstAbs}},
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
