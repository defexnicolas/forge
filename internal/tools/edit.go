package tools

import (
	"encoding/json"

	"forge/internal/patch"
)

type editFileTool struct{}

func (editFileTool) Name() string { return "edit_file" }
func (editFileTool) Description() string {
	return "Edit a file by replacing old_text with new_text after approval."
}
func (editFileTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","required":["path","old_text","new_text"],"properties":{"path":{"type":"string"},"old_text":{"type":"string"},"new_text":{"type":"string"}}}`)
}
func (editFileTool) Permission(Context, json.RawMessage) PermissionRequest {
	return PermissionRequest{Decision: PermissionAsk, Reason: "editing files changes the workspace"}
}
func (editFileTool) Run(ctx Context, input json.RawMessage) (Result, error) {
	var req struct {
		Path    string `json:"path"`
		OldText string `json:"old_text"`
		NewText string `json:"new_text"`
	}
	if err := json.Unmarshal(input, &req); err != nil {
		return Result{}, err
	}
	plan, err := patch.ExactReplace(ctx.CWD, req.Path, req.OldText, req.NewText)
	if err != nil {
		return Result{}, err
	}
	if _, err := patch.Apply(ctx.CWD, plan); err != nil {
		return Result{}, err
	}
	return Result{
		Title:        "Edit file",
		Summary:      req.Path,
		Content:      []ContentBlock{{Type: "text", Text: patch.Diff(plan), Path: req.Path}},
		ChangedFiles: []string{req.Path},
	}, nil
}

type writeFileTool struct{}

func (writeFileTool) Name() string        { return "write_file" }
func (writeFileTool) Description() string { return "Write a file in the workspace after approval." }
func (writeFileTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","required":["path","content"],"properties":{"path":{"type":"string"},"content":{"type":"string"}}}`)
}
func (writeFileTool) Permission(Context, json.RawMessage) PermissionRequest {
	return PermissionRequest{Decision: PermissionAsk, Reason: "writing files changes the workspace"}
}
func (writeFileTool) Run(ctx Context, input json.RawMessage) (Result, error) {
	var req struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(input, &req); err != nil {
		return Result{}, err
	}
	plan, err := patch.NewFile(ctx.CWD, req.Path, req.Content)
	if err != nil {
		return Result{}, err
	}
	if _, err := patch.Apply(ctx.CWD, plan); err != nil {
		return Result{}, err
	}
	return Result{
		Title:        "Write file",
		Summary:      req.Path,
		Content:      []ContentBlock{{Type: "text", Text: patch.Diff(plan), Path: req.Path}},
		ChangedFiles: []string{req.Path},
	}, nil
}

type applyPatchTool struct{}

func (applyPatchTool) Name() string { return "apply_patch" }
func (applyPatchTool) Description() string {
	return "Apply a supported unified diff after approval."
}
func (applyPatchTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","required":["patch"],"properties":{"patch":{"type":"string"}}}`)
}
func (applyPatchTool) Permission(Context, json.RawMessage) PermissionRequest {
	return PermissionRequest{Decision: PermissionAsk, Reason: "patches change the workspace"}
}
func (applyPatchTool) Run(ctx Context, input json.RawMessage) (Result, error) {
	var req struct {
		Patch string `json:"patch"`
	}
	if err := json.Unmarshal(input, &req); err != nil {
		return Result{}, err
	}
	plan, err := patch.UnifiedDiff(ctx.CWD, req.Patch)
	if err != nil {
		return Result{}, err
	}
	if _, err := patch.Apply(ctx.CWD, plan); err != nil {
		return Result{}, err
	}
	changed := make([]string, 0, len(plan.Operations))
	for _, op := range plan.Operations {
		changed = append(changed, op.Path)
	}
	return Result{
		Title:        "Apply patch",
		Summary:      "Applied unified diff",
		Content:      []ContentBlock{{Type: "text", Text: patch.Diff(plan)}},
		ChangedFiles: changed,
	}, nil
}
