package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// runSkillTool loads a SKILL.md and returns its body to the model along with
// guidance derived from the frontmatter (declared tools/models). The runtime
// does not hard-restrict tool access while a skill runs -- the policy layer is
// per-mode, not per-skill -- so the guidance is presented to the model as
// plain text constraints. The model honors them in practice; the alternative
// (forking the policy stack on every run_skill) is far more invasive than the
// value warrants.
type runSkillTool struct {
	// extraSearchDirs is appended after the user-controlled locations so a
	// project skill always shadows a plugin one with the same name.
	extraSearchDirs []string
}

func (runSkillTool) Name() string { return "run_skill" }
func (runSkillTool) Description() string {
	return "Load an installed skill (SKILL.md) and return its workflow plus declared tool/model constraints."
}
func (runSkillTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","required":["name"],"properties":{"name":{"type":"string","description":"Name of the installed skill to run."},"prompt":{"type":"string","description":"Additional context or instructions for the skill."}}}`)
}
func (runSkillTool) Permission(Context, json.RawMessage) PermissionRequest {
	return PermissionRequest{Decision: PermissionAllow}
}

func (t runSkillTool) Run(ctx Context, input json.RawMessage) (Result, error) {
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

	for _, dir := range t.searchDirs(ctx.CWD, req.Name) {
		skillFile := filepath.Join(dir, "SKILL.md")
		data, err := os.ReadFile(skillFile)
		if err != nil {
			continue
		}
		body, meta := splitFrontmatter(string(data))

		var preamble strings.Builder
		if len(meta.tools) > 0 {
			fmt.Fprintf(&preamble, "Skill %q declares tool restrictions: %s.\n", req.Name, strings.Join(meta.tools, ", "))
			preamble.WriteString("While following this skill, only call tools from that list. If you need a tool that is not listed, stop and tell the user instead of calling something else.\n\n")
		}
		if len(meta.models) > 0 {
			fmt.Fprintf(&preamble, "Skill %q is recommended for models: %s.\n\n", req.Name, strings.Join(meta.models, ", "))
		}

		full := preamble.String() + body
		if req.Prompt != "" {
			full += "\n\nUser context: " + req.Prompt
		}

		summary := "Loaded skill: " + req.Name
		if len(meta.tools) > 0 {
			summary += " (tools: " + strings.Join(meta.tools, ", ") + ")"
		}
		return Result{
			Title:   "Skill: " + req.Name,
			Summary: summary,
			Content: []ContentBlock{{Type: "text", Text: full}},
		}, nil
	}
	return Result{}, fmt.Errorf("skill not found: %s (searched workspace skills dirs, home dirs, and %d plugin dirs)", req.Name, len(t.extraSearchDirs))
}

func (t runSkillTool) searchDirs(cwd, name string) []string {
	dirs := []string{
		filepath.Join(cwd, ".agents", "skills", name),
		filepath.Join(cwd, ".forge", "skills", name),
	}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs,
			filepath.Join(home, ".codex", "skills", name),
			filepath.Join(home, ".forge", "skills", name),
		)
	}
	for _, extra := range t.extraSearchDirs {
		dirs = append(dirs, filepath.Join(extra, name))
	}
	return dirs
}

// skillFrontmatter is the subset of SKILL.md frontmatter run_skill cares about.
// Kept private to this file -- internal/skills has its own richer parser, but
// importing it here would create a cycle (skills depends on tools).
type skillFrontmatter struct {
	tools  []string
	models []string
}

func splitFrontmatter(content string) (body string, meta skillFrontmatter) {
	if !strings.HasPrefix(content, "---\n") {
		return strings.TrimSpace(content), meta
	}
	end := strings.Index(content[4:], "\n---")
	if end < 0 {
		return strings.TrimSpace(content), meta
	}
	front := content[4 : 4+end]
	body = strings.TrimSpace(content[4+end+4:])
	for _, line := range strings.Split(front, "\n") {
		line = strings.TrimSpace(line)
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		switch key {
		case "tools":
			meta.tools = parseSkillList(val)
		case "models":
			meta.models = parseSkillList(val)
		}
	}
	return body, meta
}

func parseSkillList(val string) []string {
	val = strings.Trim(val, "[]")
	if val == "" {
		return nil
	}
	parts := strings.Split(val, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// RegisterRunSkillTool installs run_skill with the given extra search dirs.
// Idempotent and overrides the bare stub registered by RegisterBuiltins.
func RegisterRunSkillTool(registry *Registry, extraSearchDirs []string) {
	registry.Register(runSkillTool{extraSearchDirs: extraSearchDirs}, "Skill")
}
