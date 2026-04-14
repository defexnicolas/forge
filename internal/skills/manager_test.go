package skills

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSkillsCLICommands(t *testing.T) {
	manager := NewManager(t.TempDir(), Options{
		CLI:          "npx",
		Repositories: []string{"vercel-labs/agent-skills"},
		Agent:        "codex",
		InstallScope: "project",
		Copy:         true,
	})

	cmd, args := manager.ListCommand("vercel-labs/agent-skills")
	if cmd != "npx" {
		t.Fatalf("cmd = %q", cmd)
	}
	wantList := []string{"skills", "add", "vercel-labs/agent-skills", "--list"}
	if !reflect.DeepEqual(args, wantList) {
		t.Fatalf("list args = %#v, want %#v", args, wantList)
	}

	cmd, args = manager.InstallCommand("vercel-labs/skills", "find-skills")
	if cmd != "npx" {
		t.Fatalf("cmd = %q", cmd)
	}
	wantInstall := []string{"skills", "add", "vercel-labs/skills", "--skill", "find-skills", "--agent", "codex", "--copy", "-y"}
	if !reflect.DeepEqual(args, wantInstall) {
		t.Fatalf("install args = %#v, want %#v", args, wantInstall)
	}

	cmd, args = manager.InstallCommandForSkill(Skill{
		Name:   "ui-ux-pro-max",
		Repo:   "nextlevelbuilder/ui-ux-pro-max-skill",
		Source: "skills.sh",
	})
	wantDirectoryInstall := []string{"skills", "add", "https://github.com/nextlevelbuilder/ui-ux-pro-max-skill", "--skill", "ui-ux-pro-max", "--agent", "codex", "--copy", "-y"}
	if cmd != "npx" || !reflect.DeepEqual(args, wantDirectoryInstall) {
		t.Fatalf("directory install = %q %#v, want npx %#v", cmd, args, wantDirectoryInstall)
	}
}

func TestDirectoryCacheUsesSkillsSHAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("offset") {
		case "":
			_, _ = w.Write([]byte(`{"skills":[{"id":"next","name":"nextjs","description":"Next work","installs":1200,"topSource":"vercel-labs/agent-skills"}],"hasMore":true}`))
		case "1":
			_, _ = w.Write([]byte(`{"skills":[{"id":"api-design","name":"api-design","installs":6500,"topSource":"supercent-io/skills-template"}],"hasMore":false}`))
		default:
			t.Fatalf("unexpected offset %q", r.URL.Query().Get("offset"))
		}
	}))
	defer server.Close()

	manager := NewManager(t.TempDir(), Options{DirectoryURL: server.URL + "/api/skills"})
	found, err := manager.RefreshDirectoryCache(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 2 {
		t.Fatalf("expected 2 directory skills, got %#v", found)
	}
	if found[0].Name != "nextjs" || found[0].Repo != "vercel-labs/agent-skills" || found[0].Source != "skills.sh" {
		t.Fatalf("unexpected first directory skill: %#v", found[0])
	}
	cached, ok := manager.ListDirectoryCached()
	if !ok || len(cached) != 2 {
		t.Fatalf("expected directory cache, got ok=%v %#v", ok, cached)
	}
	info := manager.DirectoryCacheInfo()
	if !info.Exists || info.Count != 2 || info.Repo != "skills.sh" {
		t.Fatalf("unexpected directory cache info: %#v", info)
	}
}

func TestParseDirectoryHTML(t *testing.T) {
	html := `<a href="/vercel-labs/skills/find-skills">find-skills</a>
<a href="/anthropics/skills/frontend-design">frontend-design</a>
<a href="/docs/cli">docs</a>
<a href="/api/skills">api</a>`
	found := ParseDirectoryHTML(html)
	if len(found) != 2 {
		t.Fatalf("expected 2 skills, got %#v", found)
	}
	if found[0].Name != "find-skills" || found[0].Repo != "vercel-labs/skills" || found[0].Source != "skills.sh" {
		t.Fatalf("unexpected first skill: %#v", found[0])
	}
}

func TestParseListOutput(t *testing.T) {
	output := `
Available skills in vercel-labs/agent-skills
- frontend-design - Build polished frontend experiences
skill-creator: Create high quality skills
find-skills    Find available skills
| Name | Description |
| --- | --- |
| code-review | Review changes |
[ ] test-writer - Write focused tests
`
	found := ParseListOutput("vercel-labs/agent-skills", output)
	if len(found) != 5 {
		t.Fatalf("expected 5 skills, got %#v", found)
	}
	if found[0].Name != "frontend-design" || found[0].Description != "Build polished frontend experiences" {
		t.Fatalf("unexpected first skill: %#v", found[0])
	}
	if found[1].Name != "skill-creator" {
		t.Fatalf("unexpected second skill: %#v", found[1])
	}
	if found[2].Name != "find-skills" || found[2].Repo != "vercel-labs/agent-skills" || found[2].Source != "skills-cli" {
		t.Fatalf("unexpected third skill: %#v", found[2])
	}
	if found[3].Name != "code-review" || found[3].Description != "Review changes" {
		t.Fatalf("unexpected table skill: %#v", found[3])
	}
	if found[4].Name != "test-writer" {
		t.Fatalf("unexpected checkbox skill: %#v", found[4])
	}
}

func TestManagerUsesSkillsCLIForListAndInstall(t *testing.T) {
	cwd := t.TempDir()
	var calls [][]string
	original := execCommandContext
	execCommandContext = func(ctx context.Context, command string, args ...string) *exec.Cmd {
		calls = append(calls, append([]string{command}, args...))
		helperArgs := append([]string{"-test.run=TestHelperProcess", "--"}, args...)
		cmd := exec.CommandContext(ctx, os.Args[0], helperArgs...)
		cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
		return cmd
	}
	defer func() { execCommandContext = original }()

	manager := NewManager(cwd, Options{
		CLI:          "npx",
		Repositories: []string{"vercel-labs/skills"},
		Agent:        "codex",
		InstallScope: "project",
		Copy:         true,
	})
	found, err := manager.RefreshCache(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].Name != "find-skills" || found[0].Repo != "vercel-labs/skills" {
		t.Fatalf("unexpected skills: %#v", found)
	}
	cached, ok := manager.ListCached(nil)
	if !ok || len(cached) != 1 || cached[0].Name != "find-skills" {
		t.Fatalf("expected cached skills, got ok=%v %#v", ok, cached)
	}
	installed, err := manager.InstallAndVerify(context.Background(), found[0])
	if err != nil {
		t.Fatal(err)
	}
	if installed.Name != "find-skills" || installed.InstallPath == "" {
		t.Fatalf("unexpected installed skill: %#v", installed)
	}

	want := [][]string{
		{"npx", "skills", "add", "vercel-labs/skills", "--list"},
		{"npx", "skills", "add", "vercel-labs/skills", "--skill", "find-skills", "--agent", "codex", "--copy", "-y"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
	info := manager.CacheInfo(nil)
	if len(info) != 1 || !info[0].Exists || info[0].Count != 1 {
		t.Fatalf("unexpected cache info: %#v", info)
	}
}

func TestScanLocalFindsAgentsSkills(t *testing.T) {
	cwd := t.TempDir()
	skillDir := filepath.Join(cwd, ".agents", "skills", "frontend-design")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: frontend-design\ndescription: UI work\n---\nUse care."), 0o644); err != nil {
		t.Fatal(err)
	}

	manager := NewManager(cwd, Options{})
	found := manager.ScanLocal()
	if len(found) != 1 {
		t.Fatalf("expected 1 skill, got %#v", found)
	}
	if found[0].Name != "frontend-design" || found[0].Source != "project" || !strings.Contains(found[0].InstallPath, ".agents") {
		t.Fatalf("unexpected skill: %#v", found[0])
	}
	if found[0].Description != "UI work" {
		t.Fatalf("description = %q, want frontmatter description", found[0].Description)
	}
}

func TestRemoveInstalledProjectSkill(t *testing.T) {
	cwd := t.TempDir()
	skillDir := filepath.Join(cwd, ".agents", "skills", "frontend-design")
	writeSkill(t, skillDir, "frontend-design", "UI work")

	manager := NewManager(cwd, Options{})
	removed, err := manager.RemoveInstalled("frontend-design")
	if err != nil {
		t.Fatal(err)
	}
	if removed.Name != "frontend-design" || removed.Source != "project" {
		t.Fatalf("unexpected removed skill: %#v", removed)
	}
	if _, err := os.Stat(skillDir); !os.IsNotExist(err) {
		t.Fatalf("expected skill dir removed, stat err=%v", err)
	}
}

func TestRemoveInstalledLegacyWorkspaceSkill(t *testing.T) {
	cwd := t.TempDir()
	skillDir := filepath.Join(cwd, ".forge", "skills", "commit")
	writeSkill(t, skillDir, "commit", "Commit work")

	manager := NewManager(cwd, Options{})
	removed, err := manager.RemoveInstalled("commit")
	if err != nil {
		t.Fatal(err)
	}
	if removed.Source != "legacy" {
		t.Fatalf("source = %q, want legacy", removed.Source)
	}
	if _, err := os.Stat(skillDir); !os.IsNotExist(err) {
		t.Fatalf("expected legacy skill dir removed, stat err=%v", err)
	}
}

func TestRemoveInstalledRejectsGlobalSkill(t *testing.T) {
	cwd := t.TempDir()
	home := t.TempDir()
	originalHome := userHomeDir
	userHomeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { userHomeDir = originalHome })
	writeSkill(t, filepath.Join(home, ".codex", "skills", "global-skill"), "global-skill", "Global work")

	manager := NewManager(cwd, Options{})
	if _, err := manager.RemoveInstalled("global-skill"); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("expected read-only global error, got %v", err)
	}
}

func TestRemoveInstalledRejectsEscapingPath(t *testing.T) {
	cwd := t.TempDir()
	outside := t.TempDir()
	writeSkill(t, filepath.Join(outside, "escape"), "escape", "Escape")
	err := validateRemovePath(filepath.Join(outside, "escape"), []string{
		filepath.Join(cwd, ".agents", "skills"),
		filepath.Join(cwd, ".forge", "skills"),
	})
	if err == nil {
		t.Fatal("expected escaping path to be rejected")
	}
}

func TestManagerWithoutCLIStillHasBuiltins(t *testing.T) {
	manager := NewManager(t.TempDir(), Options{})
	local := manager.ScanLocal()
	installed := map[string]bool{}
	for _, item := range local {
		installed[item.Name] = true
	}
	found := SearchAvailable("", installed)
	if len(found) == 0 {
		t.Fatal("expected built-in skills")
	}
	for _, skill := range found {
		if skill.Source != "builtin" {
			t.Fatalf("expected builtin source, got %#v", skill)
		}
	}
}

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) > 0 {
		args = args[1:]
	}
	if len(args) >= 4 && args[0] == "skills" && args[1] == "add" && args[3] == "--list" {
		_, _ = os.Stdout.WriteString("- find-skills - Find skills for the task\n")
		os.Exit(0)
	}
	if len(args) >= 6 && args[0] == "skills" && args[1] == "add" && args[3] == "--skill" {
		dir := filepath.Join(".", ".agents", "skills", args[4])
		if err := os.MkdirAll(dir, 0o755); err != nil {
			_, _ = os.Stderr.WriteString(err.Error())
			os.Exit(1)
		}
		content := "---\nname: " + args[4] + "\ndescription: installed test skill\n---\nUse the installed skill."
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
			_, _ = os.Stderr.WriteString(err.Error())
			os.Exit(1)
		}
		_, _ = os.Stdout.WriteString("installed\n")
		os.Exit(0)
	}
	os.Exit(2)
}

func writeSkill(t *testing.T, dir, name, description string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\ndescription: " + description + "\n---\nBody."
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
