package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
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

func TestRunSkillRendersSteps(t *testing.T) {
	cwd := t.TempDir()
	writeSkillFile(t,
		filepath.Join(cwd, ".forge", "skills", "checklist"),
		"name: checklist\ndescription: do these things\nsteps:\n  - read the file\n  - write the test\n  - run go test ./...",
		"Body explaining the workflow.",
	)
	tool := runSkillTool{}
	result, err := tool.Run(Context{CWD: cwd}, json.RawMessage(`{"name":"checklist"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	body := result.Content[0].Text
	if !strings.Contains(body, "## Steps") {
		t.Errorf("steps section missing: %q", body)
	}
	for i, expected := range []string{"1. read the file", "2. write the test", "3. run go test ./..."} {
		if !strings.Contains(body, expected) {
			t.Errorf("step %d not numbered correctly: missing %q in %q", i+1, expected, body)
		}
	}
	if !strings.Contains(result.Summary, "(3 steps)") {
		t.Errorf("summary should mention step count: %q", result.Summary)
	}
}

func TestRunSkillRunsScript(t *testing.T) {
	cwd := t.TempDir()
	skillDir := filepath.Join(cwd, ".forge", "skills", "scripted")
	scriptName := "run.sh"
	scriptBody := "#!/bin/sh\necho hello-from-skill\n"
	if runtime.GOOS == "windows" {
		scriptName = "run.bat"
		scriptBody = "@echo off\r\necho hello-from-skill\r\n"
	}
	writeSkillFile(t, skillDir,
		"name: scripted\ndescription: runs a script\nscript: "+scriptName,
		"Body before script.",
	)
	if err := os.WriteFile(filepath.Join(skillDir, scriptName), []byte(scriptBody), 0o755); err != nil {
		t.Fatal(err)
	}

	tool := runSkillTool{}
	result, err := tool.Run(Context{CWD: cwd}, json.RawMessage(`{"name":"scripted"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	body := result.Content[0].Text
	if !strings.Contains(body, "## Script output") {
		t.Errorf("script output section missing: %q", body)
	}
	if !strings.Contains(body, "hello-from-skill") {
		t.Errorf("script stdout not captured: %q", body)
	}
	if !strings.Contains(result.Summary, "script: ran") {
		t.Errorf("summary should mention script ran: %q", result.Summary)
	}
}

func TestRunSkillScriptPathEscapeRefused(t *testing.T) {
	cwd := t.TempDir()
	skillDir := filepath.Join(cwd, ".forge", "skills", "escapee")
	writeSkillFile(t, skillDir,
		"name: escapee\ndescription: tries to break out\nscript: ../../etc/passwd",
		"don't.",
	)
	tool := runSkillTool{}
	result, err := tool.Run(Context{CWD: cwd}, json.RawMessage(`{"name":"escapee"}`))
	if err != nil {
		t.Fatalf("Run should succeed (script error is captured, not propagated): %v", err)
	}
	if !strings.Contains(result.Content[0].Text, "Script error") {
		t.Errorf("expected script error captured in body, got: %q", result.Content[0].Text)
	}
	if !strings.Contains(result.Content[0].Text, "escapes the skill directory") {
		t.Errorf("expected path-escape error message, got: %q", result.Content[0].Text)
	}
}

func TestRunSkillPermissionIsAsk(t *testing.T) {
	tool := runSkillTool{}
	pr := tool.Permission(Context{}, json.RawMessage(`{"name":"x"}`))
	if pr.Decision != PermissionAsk {
		t.Errorf("expected PermissionAsk, got %v", pr.Decision)
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
