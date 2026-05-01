package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkillFile(t *testing.T, dir, frontmatter, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := body
	if frontmatter != "" {
		content = "---\n" + frontmatter + "\n---\n" + body
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRunSkillEmitsToolGuidance(t *testing.T) {
	cwd := t.TempDir()
	writeSkillFile(t,
		filepath.Join(cwd, ".forge", "skills", "review"),
		"name: review\ndescription: Review code\ntools: [read_file, search_text, git_diff]",
		"Step 1: read the diff.\nStep 2: comment.",
	)

	tool := runSkillTool{}
	result, err := tool.Run(Context{CWD: cwd}, json.RawMessage(`{"name":"review"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	body := result.Content[0].Text
	if !strings.Contains(body, "tool restrictions: read_file, search_text, git_diff") {
		t.Errorf("missing tool restriction preamble: %q", body)
	}
	if !strings.Contains(body, "Step 1: read the diff.") {
		t.Errorf("missing skill body: %q", body)
	}
	if !strings.Contains(result.Summary, "tools: read_file, search_text, git_diff") {
		t.Errorf("summary should reflect tool list: %q", result.Summary)
	}
}

func TestRunSkillNoFrontmatterTools(t *testing.T) {
	cwd := t.TempDir()
	writeSkillFile(t,
		filepath.Join(cwd, ".forge", "skills", "freeform"),
		"name: freeform\ndescription: do whatever",
		"Just do it.",
	)
	tool := runSkillTool{}
	result, err := tool.Run(Context{CWD: cwd}, json.RawMessage(`{"name":"freeform"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(result.Content[0].Text, "tool restrictions") {
		t.Errorf("must not emit a tool-restriction preamble when frontmatter is silent: %q", result.Content[0].Text)
	}
	if strings.Contains(result.Summary, "tools:") {
		t.Errorf("summary must not list tools when frontmatter is silent: %q", result.Summary)
	}
}

func TestRunSkillFromPluginDir(t *testing.T) {
	cwd := t.TempDir()
	pluginSkills := filepath.Join(cwd, ".forge", "plugins", "demo", "skills")
	writeSkillFile(t,
		filepath.Join(pluginSkills, "shipped"),
		"name: shipped\ndescription: from a plugin\ntools: [read_file]",
		"Plugin body.",
	)

	tool := runSkillTool{extraSearchDirs: []string{pluginSkills}}
	result, err := tool.Run(Context{CWD: cwd}, json.RawMessage(`{"name":"shipped"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(result.Content[0].Text, "Plugin body.") {
		t.Errorf("plugin-dir skill not loaded: %q", result.Content[0].Text)
	}
}

func TestRunSkillProjectShadowsPlugin(t *testing.T) {
	cwd := t.TempDir()
	pluginSkills := filepath.Join(cwd, ".forge", "plugins", "demo", "skills")
	writeSkillFile(t,
		filepath.Join(pluginSkills, "shared"),
		"name: shared\ndescription: from plugin",
		"plugin wins? no.",
	)
	writeSkillFile(t,
		filepath.Join(cwd, ".forge", "skills", "shared"),
		"name: shared\ndescription: from project",
		"project wins.",
	)

	tool := runSkillTool{extraSearchDirs: []string{pluginSkills}}
	result, err := tool.Run(Context{CWD: cwd}, json.RawMessage(`{"name":"shared"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(result.Content[0].Text, "project wins.") {
		t.Errorf("project skill should win shadow: %q", result.Content[0].Text)
	}
}

func TestRunSkillMissingErrors(t *testing.T) {
	tool := runSkillTool{}
	if _, err := tool.Run(Context{CWD: t.TempDir()}, json.RawMessage(`{"name":"nope"}`)); err == nil {
		t.Fatal("expected error for missing skill")
	}
	if _, err := tool.Run(Context{CWD: t.TempDir()}, json.RawMessage(`{}`)); err == nil {
		t.Fatal("expected error for missing name")
	}
}

func TestRegisterRunSkillToolOverridesBuiltin(t *testing.T) {
	registry := NewRegistry()
	RegisterBuiltins(registry)
	RegisterRunSkillTool(registry, []string{"/tmp/plugin1/skills"})
	tool, ok := registry.Get("run_skill")
	if !ok {
		t.Fatal("run_skill missing")
	}
	rs, ok := tool.(runSkillTool)
	if !ok {
		t.Fatalf("expected runSkillTool, got %T", tool)
	}
	if len(rs.extraSearchDirs) != 1 || rs.extraSearchDirs[0] != "/tmp/plugin1/skills" {
		t.Errorf("extra dirs not propagated: %v", rs.extraSearchDirs)
	}
}
