package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

// clawWorkspaceNoteTool lets the LLM update its own markdown
// "personality" files mid-conversation. Different from claw_remember
// (structured fact for retrieval) — this is for prose-style
// observations the LLM judges worth keeping next to the file that owns
// the topic (e.g. "user prefers terser replies" → SOUL.md).
type clawWorkspaceNoteTool struct {
	append func(ctx context.Context, file, note string) (string, error)
}

func (clawWorkspaceNoteTool) Name() string { return "claw_workspace_note" }
func (clawWorkspaceNoteTool) Description() string {
	return "Append a short prose note to one of Claw's markdown personality files. Use when the user reveals something worth keeping next to a specific topic — e.g. 'user prefers terser replies' → SOUL.md, 'user lives in Barranquilla' → USER.md, 'follow up with Sebastián tomorrow' → MEMORY.md, 'avoid emoji on whatsapp' → TOOLS.md. For structured factual recall (allergies, recurring details), prefer claw_remember instead."
}
func (clawWorkspaceNoteTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","required":["file","note"],"properties":{"file":{"type":"string","enum":["MEMORY.md","SOUL.md","USER.md","TOOLS.md","IDENTITY.md"],"description":"Which markdown file to append to. AGENTS.md and HEARTBEAT.md are operator-edited and not writable here."},"note":{"type":"string","description":"One short line of prose. Will be added as a bullet — no need to prefix with '- '."}}}`)
}
func (clawWorkspaceNoteTool) Permission(_ Context, _ json.RawMessage) PermissionRequest {
	return PermissionRequest{Decision: PermissionAllow, Reason: "Edits a local markdown file in Claw's workspace — no external side effects"}
}
func (t clawWorkspaceNoteTool) Run(ctx Context, input json.RawMessage) (Result, error) {
	if t.append == nil {
		return Result{Title: "claw_workspace_note", Summary: "workspace not wired"}, errors.New("claw_workspace_note: no append closure registered")
	}
	var req struct {
		File string `json:"file"`
		Note string `json:"note"`
	}
	if err := json.Unmarshal(input, &req); err != nil {
		return Result{}, err
	}
	file := strings.TrimSpace(req.File)
	note := strings.TrimSpace(req.Note)
	if file == "" || note == "" {
		return Result{Title: "claw_workspace_note", Summary: "file and note are required"}, errors.New("file and note are required")
	}
	canonical, err := t.append(ctx.Context, file, note)
	if err != nil {
		return Result{
			Title:   "claw_workspace_note",
			Summary: "append failed: " + err.Error(),
			Content: []ContentBlock{{Type: "text", Text: err.Error()}},
		}, nil
	}
	summary := "appended to " + canonical
	return Result{
		Title:   "claw_workspace_note",
		Summary: summary,
		Content: []ContentBlock{{Type: "text", Text: summary}},
	}, nil
}

// RegisterClawWorkspaceNoteTool wires the append closure into the
// registry. Pass nil for a placeholder that surfaces a clear error.
func RegisterClawWorkspaceNoteTool(reg *Registry, append func(ctx context.Context, file, note string) (string, error)) {
	reg.Register(clawWorkspaceNoteTool{append: append}, "ClawWorkspaceNote")
}
