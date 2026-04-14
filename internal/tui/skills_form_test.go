package tui

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"forge/internal/skills"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

func TestSkillsFormUsesConfiguredRepoAndMarksInstalled(t *testing.T) {
	cwd := t.TempDir()
	cli := fakeSkillsCLI(t, cwd)
	skillDir := filepath.Join(cwd, ".agents", "skills", "repo-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: repo-skill\n---\nInstalled."), 0o644); err != nil {
		t.Fatal(err)
	}

	manager := skills.NewManager(cwd, skills.Options{
		CLI:          cli,
		Repositories: []string{"owner/repo"},
		Agent:        "codex",
		InstallScope: "project",
		Copy:         true,
	})
	form, cmd := newSkillsForm(cwd, manager, DefaultTheme(), []string{"owner/repo"}, false)
	if !form.loading {
		t.Fatal("expected form to load missing cache in background")
	}
	msg := cmd()
	var updateCmd tea.Cmd
	form, updateCmd = form.Update(msg)
	if updateCmd != nil {
		t.Fatal("unexpected update command")
	}
	if form.loading {
		t.Fatal("expected form loading to finish")
	}
	if len(form.allResults) == 0 {
		t.Fatal("expected skills from fake CLI")
	}
	got := form.allResults[0]
	if got.Name != "repo-skill" || got.Repo != "owner/repo" || got.Source != "skills-cli" {
		t.Fatalf("unexpected skill: %#v", got)
	}
	if !got.Installed {
		t.Fatalf("expected skill to be marked installed: %#v", got)
	}
}

func TestSkillsFormScrollsVisibleWindow(t *testing.T) {
	var items []skills.Skill
	for i := 0; i < 20; i++ {
		items = append(items, skills.Skill{
			Name:        "skill-" + strconv.Itoa(i),
			Description: "desc",
			Repo:        "owner/repo",
			Source:      "skills-cli",
		})
	}
	form := skillsForm{
		search:     textinput.New(),
		allResults: items,
		filtered:   items,
		theme:      DefaultTheme(),
	}
	for i := 0; i < 15; i++ {
		var cmd tea.Cmd
		form, cmd = form.Update(tea.KeyMsg{Type: tea.KeyDown})
		if cmd != nil {
			t.Fatal("unexpected command")
		}
	}
	if form.selected != 15 {
		t.Fatalf("selected = %d, want 15", form.selected)
	}
	if form.offset == 0 {
		t.Fatal("expected offset to move as selection leaves visible window")
	}
	view := stripAnsi(form.View())
	if !strings.Contains(view, "skill-15") {
		t.Fatalf("expected selected skill in view:\n%s", view)
	}
}

func TestSkillsFormInstalledTabRemoveFlow(t *testing.T) {
	cwd := t.TempDir()
	skillDir := filepath.Join(cwd, ".agents", "skills", "installed-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: installed-skill\ndescription: Installed test\n---\nBody."), 0o644); err != nil {
		t.Fatal(err)
	}
	manager := skills.NewManager(cwd, skills.Options{})
	form := skillsForm{
		search:           textinput.New(),
		allResults:       []skills.Skill{{Name: "installed-skill", Description: "Available", Repo: "owner/repo", Source: "skills.sh", Installed: true, InstallPath: skillDir}},
		installedResults: manager.ScanLocal(),
		manager:          manager,
		theme:            DefaultTheme(),
	}
	form.applyFilter()

	var cmd tea.Cmd
	form, cmd = form.Update(tea.KeyMsg{Type: tea.KeyRight})
	if cmd != nil {
		t.Fatal("unexpected command")
	}
	if form.tab != skillsTabInstalled || len(form.filtered) != 1 {
		t.Fatalf("expected installed tab with one item, tab=%v filtered=%#v", form.tab, form.filtered)
	}
	form, _ = form.Update(tea.KeyMsg{Type: tea.KeyDelete})
	if form.confirmRemove != "installed-skill" {
		t.Fatalf("confirmRemove = %q", form.confirmRemove)
	}
	form, _ = form.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if form.confirmRemove != "" || form.done {
		t.Fatalf("expected inline confirm cancel without closing, confirm=%q done=%v", form.confirmRemove, form.done)
	}
	form, _ = form.Update(tea.KeyMsg{Type: tea.KeyDelete})
	form, _ = form.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if form.confirmRemove != "" || form.errMsg != "" {
		t.Fatalf("unexpected remove state confirm=%q err=%q", form.confirmRemove, form.errMsg)
	}
	if len(form.installedResults) != 0 || len(form.filtered) != 0 {
		t.Fatalf("expected installed list to refresh empty, installed=%#v filtered=%#v", form.installedResults, form.filtered)
	}
	if form.allResults[0].Installed {
		t.Fatalf("expected available result to be marked uninstalled: %#v", form.allResults[0])
	}
	if _, err := os.Stat(skillDir); !os.IsNotExist(err) {
		t.Fatalf("expected skill dir removed, stat err=%v", err)
	}
}

func TestSkillsFormInstalledTabGlobalReadOnly(t *testing.T) {
	manager := skills.NewManager(t.TempDir(), skills.Options{})
	form := skillsForm{
		search:           textinput.New(),
		installedResults: []skills.Skill{{Name: "global-skill", Source: "global", Installed: true, InstallPath: "C:/Users/example/.codex/skills/global-skill"}},
		manager:          manager,
		theme:            DefaultTheme(),
		tab:              skillsTabInstalled,
	}
	form.applyFilter()
	form, _ = form.Update(tea.KeyMsg{Type: tea.KeyDelete})
	if form.confirmRemove != "" {
		t.Fatalf("global skill should not enter remove confirm: %q", form.confirmRemove)
	}
	if !strings.Contains(form.notice, "read-only") {
		t.Fatalf("expected read-only notice, got %q", form.notice)
	}
}

func TestSkillsFormFilterAppliesToActiveTab(t *testing.T) {
	manager := skills.NewManager(t.TempDir(), skills.Options{})
	search := textinput.New()
	search.SetValue("installed")
	form := skillsForm{
		search:           search,
		allResults:       []skills.Skill{{Name: "available-skill", Source: "skills.sh"}},
		installedResults: []skills.Skill{{Name: "installed-skill", Source: "project", Installed: true}},
		manager:          manager,
		theme:            DefaultTheme(),
	}
	form.applyFilter()
	if len(form.filtered) != 0 {
		t.Fatalf("available tab should not match installed query: %#v", form.filtered)
	}
	form, _ = form.Update(tea.KeyMsg{Type: tea.KeyRight})
	if len(form.filtered) != 1 || form.filtered[0].Name != "installed-skill" {
		t.Fatalf("installed tab should match installed query: %#v", form.filtered)
	}
}

func fakeSkillsCLI(t *testing.T, dir string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		path := filepath.Join(dir, "fake-skills.cmd")
		script := "@echo off\r\nif \"%1\"==\"skills\" if \"%2\"==\"add\" if \"%4\"==\"--list\" echo - repo-skill - Repo listed skill\r\nexit /b 0\r\n"
		if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	path := filepath.Join(dir, "fake-skills")
	script := "#!/bin/sh\nif [ \"$1\" = \"skills\" ] && [ \"$2\" = \"add\" ] && [ \"$4\" = \"--list\" ]; then\n  echo '- repo-skill - Repo listed skill'\n  exit 0\nfi\nexit 0\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(path, "\\", "/")
}
