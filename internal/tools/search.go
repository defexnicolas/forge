package tools

import (
	"context"
	"encoding/json"
	"fmt"
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
	matches, err := runSearchText(context.Background(), ctx.CWD, req.Query, req.Limit)
	if err != nil {
		return Result{}, err
	}
	backend := "ripgrep"
	if ripgrepPath() == "" {
		backend = "go-fallback"
	}
	return Result{
		Title:   "Search text",
		Summary: fmt.Sprintf("%d matches (%s)", len(matches), backend),
		Content: []ContentBlock{{Type: "text", Text: strings.Join(matches, "\n")}},
	}, nil
}
