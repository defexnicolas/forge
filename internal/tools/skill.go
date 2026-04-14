package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type runSkillTool struct{}

func (runSkillTool) Name() string        { return "run_skill" }
func (runSkillTool) Description() string { return "Load and execute an installed skill workflow." }
func (runSkillTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","required":["name"],"properties":{"name":{"type":"string","description":"Name of the installed skill to run."},"prompt":{"type":"string","description":"Additional context or instructions for the skill."}}}`)
}
func (runSkillTool) Permission(Context, json.RawMessage) PermissionRequest {
	return PermissionRequest{Decision: PermissionAllow}
}
func (runSkillTool) Run(ctx Context, input json.RawMessage) (Result, error) {
	var req struct {
		Name   string `json:"name"`
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal(input, &req); err != nil {
		return Result{}, err
	}
	if req.Name == "" {
		return Result{}, fmt.Errorf("skill name is required")
	}

	// Search for the skill in known directories.
	searchDirs := []string{
		filepath.Join(ctx.CWD, ".agents", "skills", req.Name),
		filepath.Join(ctx.CWD, ".forge", "skills", req.Name),
	}
	if home, err := os.UserHomeDir(); err == nil {
		searchDirs = append(searchDirs,
			filepath.Join(home, ".codex", "skills", req.Name),
			filepath.Join(home, ".forge", "skills", req.Name),
		)
	}

	for _, dir := range searchDirs {
		skillFile := filepath.Join(dir, "SKILL.md")
		data, err := os.ReadFile(skillFile)
		if err != nil {
			continue
		}
		content := string(data)
		// Strip frontmatter.
		if strings.HasPrefix(content, "---\n") {
			if end := strings.Index(content[4:], "\n---"); end >= 0 {
				content = strings.TrimSpace(content[4+end+4:])
			}
		}
		summary := "Loaded skill: " + req.Name
		if req.Prompt != "" {
			content += "\n\nUser context: " + req.Prompt
			summary += " with prompt"
		}
		return Result{
			Title:   "Skill: " + req.Name,
			Summary: summary,
			Content: []ContentBlock{{Type: "text", Text: content}},
		}, nil
	}
	return Result{}, fmt.Errorf("skill not found: %s (searched in .agents/skills/, .forge/skills/)", req.Name)
}
