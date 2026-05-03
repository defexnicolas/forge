package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// runSkillTool loads a SKILL.md and returns its body to the model along with
// guidance derived from the frontmatter. Three frontmatter fields shape the
// output:
//
//   - tools:  []string  -> soft restriction guidance prepended to the body.
//                          Hard enforcement would mean forking the policy
//                          stack per call -- the model honors the preamble
//                          in practice, which is enough.
//   - models: []string  -> advisory recommendation prepended to the body.
//   - steps:  []string  -> rendered as a numbered checklist appended to
//                          the body so the model has an explicit ordering
//                          even if the prose is loose.
//   - script: string    -> path (relative to the skill dir) to a script the
//                          tool actually executes. stdout is captured into
//                          a fenced block in the output. Permission is
//                          PermissionAsk because this runs arbitrary code.
type runSkillTool struct {
	// extraSearchDirs is appended after the user-controlled locations so a
	// project skill always shadows a plugin one with the same name.
	extraSearchDirs []string
}

func (runSkillTool) Name() string { return "run_skill" }
func (runSkillTool) Description() string {
	return "Load an installed skill (SKILL.md). Renders body, declared tool/model constraints, numbered steps, and the captured stdout of the skill's script if one is declared."
}
func (runSkillTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","required":["name"],"properties":{"name":{"type":"string","description":"Name of the installed skill to run."},"prompt":{"type":"string","description":"Additional context or instructions for the skill."}}}`)
}

// Permission is conservative: a skill can ship a script that runs arbitrary
// code, and run_skill alone would otherwise execute it with no prompt. We
// always ask. Skills without a script will look identical to a noop ask
// from the user's POV; the alternative (peeking at the SKILL.md from
// Permission) introduces a stat call that may not even find the skill.
func (runSkillTool) Permission(_ Context, _ json.RawMessage) PermissionRequest {
	return PermissionRequest{
		Decision: PermissionAsk,
		Reason:   "run_skill can execute a script declared in the skill's frontmatter. Approve to load and (if a script: is declared) run it.",
	}
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

		var stepsBlock string
		if len(meta.steps) > 0 {
			var sb strings.Builder
			sb.WriteString("\n\n## Steps\n\n")
			for i, step := range meta.steps {
				fmt.Fprintf(&sb, "%d. %s\n", i+1, step)
			}
			stepsBlock = sb.String()
		}

		var scriptBlock string
		var scriptErr error
		if meta.script != "" {
			scriptBlock, scriptErr = runSkillScript(dir, meta.script)
		}

		full := preamble.String() + body + stepsBlock
		if scriptBlock != "" {
			full += "\n\n## Script output\n\n```\n" + scriptBlock + "\n```\n"
		}
		if scriptErr != nil {
			full += "\n\n_Script error:_ " + scriptErr.Error()
		}
		if req.Prompt != "" {
			full += "\n\nUser context: " + req.Prompt
		}

		summary := "Loaded skill: " + req.Name
		if len(meta.tools) > 0 {
			summary += " (tools: " + strings.Join(meta.tools, ", ") + ")"
		}
		if len(meta.steps) > 0 {
			summary += fmt.Sprintf(" (%d steps)", len(meta.steps))
		}
		if meta.script != "" {
			if scriptErr != nil {
				summary += " (script: error)"
			} else {
				summary += " (script: ran)"
			}
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
			// Claude Code-style location — scanned last so forge/codex
			// skills shadow any same-named one under ~/.claude. Keeps
			// `git clone … ~/.claude/skills/<pack>` workflows usable
			// without renaming.
			filepath.Join(home, ".claude", "skills", name),
		)
	}
	for _, extra := range t.extraSearchDirs {
		dirs = append(dirs, filepath.Join(extra, name))
	}
	return dirs
}

// runSkillScript resolves `script` against the skill dir, refuses any path
// that escapes that dir, and executes it with a 30s timeout. stdout (and
// stderr if non-empty) is captured and trimmed to 16 KB so a chatty script
// can't blow up the response. The script must already be executable; we do
// not chmod it.
func runSkillScript(skillDir, script string) (string, error) {
	if filepath.IsAbs(script) {
		return "", fmt.Errorf("skill script path must be relative to the skill directory")
	}
	resolved, err := filepath.Abs(filepath.Join(skillDir, script))
	if err != nil {
		return "", err
	}
	skillAbs, err := filepath.Abs(skillDir)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(skillAbs, resolved)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("skill script path escapes the skill directory: %s", script)
	}
	if _, err := os.Stat(resolved); err != nil {
		return "", fmt.Errorf("skill script not found: %s", script)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, resolved)
	if runtime.GOOS == "windows" && filepath.Ext(resolved) == ".sh" {
		// On Windows let the user keep .sh wrappers if they have bash on PATH.
		cmd = exec.CommandContext(ctx, "bash", resolved)
	}
	cmd.Dir = skillDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	out := stdout.String()
	if msg := strings.TrimSpace(stderr.String()); msg != "" {
		if out != "" {
			out += "\n--- stderr ---\n"
		}
		out += msg
	}
	const maxOut = 16 * 1024
	if len(out) > maxOut {
		out = out[:maxOut] + "\n[output truncated]"
	}
	return out, runErr
}

// skillFrontmatter is the subset of SKILL.md frontmatter run_skill cares about.
// Kept private to this file -- internal/skills has its own richer parser, but
// importing it here would create a cycle (skills depends on tools).
type skillFrontmatter struct {
	tools  []string
	models []string
	steps  []string
	script string
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

	// Two passes: line-by-line for scalar keys (tools, models, script) and a
	// block-aware pass for `steps:\n  - ...` because steps is the only field
	// that's commonly multi-line.
	parseScalarsAndSteps(front, &meta)
	return body, meta
}

func parseScalarsAndSteps(front string, meta *skillFrontmatter) {
	lines := strings.Split(front, "\n")
	inSteps := false
	for _, raw := range lines {
		line := strings.TrimRight(raw, " \t")
		trim := strings.TrimSpace(line)
		if inSteps {
			if strings.HasPrefix(trim, "- ") {
				meta.steps = append(meta.steps, strings.TrimSpace(strings.TrimPrefix(trim, "- ")))
				continue
			}
			// Anything that is not a "- ..." continuation closes the steps list.
			if trim != "" {
				inSteps = false
			} else {
				continue
			}
		}
		key, val, ok := strings.Cut(trim, ":")
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
		case "script":
			meta.script = strings.Trim(val, "\"'")
		case "steps":
			if val == "" {
				inSteps = true
			} else {
				meta.steps = parseSkillList(val)
			}
		}
	}
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
