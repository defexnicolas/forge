package tui

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// withFakeHome points HOME / USERPROFILE at a temp dir so
// skillCommandSuggestions's home-scoped search hits a known empty state
// instead of whatever the developer happens to have under
// ~/.forge/skills. Restored on cleanup.
func withFakeHome(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
	return tmp
}

func TestSuggestIncludesInstalledSkill(t *testing.T) {
	withFakeHome(t)
	cwd := t.TempDir()
	skillDir := filepath.Join(cwd, ".forge", "skills", "foo-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("body"), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}

	got := Suggest("/foo", cwd)
	if !slices.Contains(got, "/foo-skill") {
		t.Fatalf("expected /foo-skill in suggestions, got %v", got)
	}
}

func TestSuggestSkipsSkillShadowedByBuiltin(t *testing.T) {
	withFakeHome(t)
	cwd := t.TempDir()
	// A skill named "review" — should not appear because /review is built-in.
	skillDir := filepath.Join(cwd, ".forge", "skills", "review")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("body"), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}

	got := Suggest("/rev", cwd)
	// /review must appear once (from the built-in list) — not twice.
	count := 0
	for _, m := range got {
		if m == "/review" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly one /review entry, got %d in %v", count, got)
	}
}

func TestSuggestIgnoresSkillDirWithoutSkillMD(t *testing.T) {
	withFakeHome(t)
	cwd := t.TempDir()
	// Directory exists but has no SKILL.md — must NOT register.
	if err := os.MkdirAll(filepath.Join(cwd, ".forge", "skills", "incomplete"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	got := Suggest("/inc", cwd)
	if slices.Contains(got, "/incomplete") {
		t.Fatalf("did not expect /incomplete (no SKILL.md), got %v", got)
	}
}

func TestSuggestPicksUpClaudeStyleSkill(t *testing.T) {
	home := withFakeHome(t)
	cwd := t.TempDir()
	// Skill installed under ~/.claude/skills/ — the Claude Code location
	// gstack and similar packs use. Should be discoverable now.
	skillDir := filepath.Join(home, ".claude", "skills", "office-hours")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("body"), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	got := Suggest("/off", cwd)
	if !slices.Contains(got, "/office-hours") {
		t.Fatalf("expected /office-hours from ~/.claude/skills/, got %v", got)
	}
}

func TestStripSkillFrontmatter(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"no frontmatter", "Just a body.\n", "Just a body.\n"},
		{"unix frontmatter", "---\nname: x\n---\nBody.\n", "Body.\n"},
		{"crlf frontmatter", "---\r\nname: x\r\n---\r\nBody.\r\n", "Body.\r\n"},
		{"empty body", "---\nname: x\n---\n", ""},
		{"unterminated frontmatter falls through", "---\nname: x\n", "---\nname: x\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripSkillFrontmatter(tc.in); got != tc.want {
				t.Fatalf("stripSkillFrontmatter(%q) = %q; want %q", tc.in, got, tc.want)
			}
		})
	}
}
