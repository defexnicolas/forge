package tools

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type searchTextTool struct{}

func (searchTextTool) Name() string        { return "search_text" }
func (searchTextTool) Description() string { return "Search text in workspace files." }
func (searchTextTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","required":["query"],"properties":{"query":{"type":"string"},"limit":{"type":"integer"}}}`)
}
func (searchTextTool) Permission(Context, json.RawMessage) PermissionRequest {
	return PermissionRequest{Decision: PermissionAllow}
}
func (searchTextTool) Run(ctx Context, input json.RawMessage) (Result, error) {
	var req struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(input, &req); err != nil {
		return Result{}, err
	}
	if req.Limit <= 0 {
		req.Limit = 50
	}

	var matches []string
	err := filepath.WalkDir(ctx.CWD, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || len(matches) >= req.Limit {
			return err
		}
		file, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer file.Close()
		scanner := bufio.NewScanner(file)
		lineNumber := 0
		for scanner.Scan() {
			lineNumber++
			if strings.Contains(scanner.Text(), req.Query) {
				rel, _ := filepath.Rel(ctx.CWD, path)
				matches = append(matches, fmt.Sprintf("%s:%d:%s", rel, lineNumber, scanner.Text()))
				if len(matches) >= req.Limit {
					break
				}
			}
		}
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	return Result{
		Title:   "Search text",
		Summary: fmt.Sprintf("%d matches", len(matches)),
		Content: []ContentBlock{{Type: "text", Text: strings.Join(matches, "\n")}},
	}, nil
}
