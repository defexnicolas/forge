package contextbuilder

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forge/internal/config"
	"forge/internal/skills"
	"forge/internal/tools"
	"forge/internal/yarn"
)

func TestBuildWithAgentsMD(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte("Use small patches."), 0o644); err != nil {
		t.Fatal(err)
	}

	builder := NewBuilder(cwd, config.Defaults(), tools.NewRegistry())
	snapshot := builder.Build("hello")

	rendered := snapshot.Render()
	if !strings.Contains(rendered, "Use small patches.") {
		t.Fatalf("expected AGENTS.md content in context, got:\n%s", rendered)
	}
}

func TestBuildWithoutAgentsMD(t *testing.T) {
	cwd := t.TempDir()
	builder := NewBuilder(cwd, config.Defaults(), tools.NewRegistry())
	snapshot := builder.Build("hello")
	if len(snapshot.Items) != 0 {
		t.Fatalf("expected no items, got %#v", snapshot.Items)
	}
}

func TestMentions(t *testing.T) {
	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "docs", "ARCHITECTURE.md"), []byte("architecture"), 0o644); err != nil {
		t.Fatal(err)
	}

	builder := NewBuilder(cwd, config.Defaults(), tools.NewRegistry())
	snapshot := builder.Build("resume @docs/ARCHITECTURE.md")

	rendered := snapshot.Render()
	if !strings.Contains(rendered, "architecture") {
		t.Fatalf("expected mentioned file content in context, got:\n%s", rendered)
	}
}

type fakeHistory struct {
	text      string
	lastLimit int
}

func (f *fakeHistory) ContextText(limit int) string {
	f.lastLimit = limit
	return f.text
}

func TestYarnIncludesSessionHistory(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Context.Engine = "yarn"

	builder := NewBuilder(cwd, cfg, tools.NewRegistry())
	builder.History = &fakeHistory{text: "2026-04-13T00:00:00Z user\nDiscussed plugin loading."}
	snapshot := builder.Build("what did we discuss about plugins?")

	rendered := snapshot.Render()
	if !strings.Contains(rendered, "Discussed plugin loading.") {
		t.Fatalf("expected session history in yarn context, got:\n%s", rendered)
	}
}

func TestSimpleEngineSkipsYarnSessionHistory(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Context.Engine = "simple"

	builder := NewBuilder(cwd, cfg, tools.NewRegistry())
	builder.History = &fakeHistory{text: "hidden session history"}
	snapshot := builder.Build("hello")

	if strings.Contains(snapshot.Render(), "hidden session history") {
		t.Fatalf("simple context should not include yarn session history:\n%s", snapshot.Render())
	}
}

func TestYarnRespectsMaxNodes(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Context.Engine = "yarn"
	cfg.Context.Yarn.MaxNodes = 1
	builder := NewBuilder(cwd, cfg, tools.NewRegistry())
	for _, node := range []string{"alpha one", "alpha two", "alpha three"} {
		if err := builder.Yarn.Upsert(yarnNode("note", node, node)); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := builder.Build("alpha")
	count := 0
	for _, item := range snapshot.Items {
		if strings.HasPrefix(item.Kind, "yarn:") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("selected yarn nodes = %d, want 1: %#v", count, snapshot.Items)
	}
}

func TestYarnRespectsMaxFileBytes(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "long.txt"), []byte(strings.Repeat("a", 200)), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Context.Engine = "simple"
	cfg.Context.Yarn.MaxFileBytes = 20
	builder := NewBuilder(cwd, cfg, tools.NewRegistry())
	snapshot := builder.Build("@long.txt")
	rendered := snapshot.Render()
	if !strings.Contains(rendered, "[truncated]") {
		t.Fatalf("expected truncation, got:\n%s", rendered)
	}
	if strings.Count(rendered, "a") > 80 {
		t.Fatalf("expected short truncated content, got:\n%s", rendered)
	}
}

func TestYarnUsesConfiguredHistoryEvents(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Defaults()
	cfg.Context.Engine = "yarn"
	cfg.Context.Yarn.HistoryEvents = 3
	history := &fakeHistory{text: "history alpha"}
	builder := NewBuilder(cwd, cfg, tools.NewRegistry())
	builder.History = history
	_ = builder.Build("alpha")
	if history.lastLimit != 3 {
		t.Fatalf("history limit = %d, want 3", history.lastLimit)
	}
}

func TestYarnPinsAlwaysIncludedWithoutQueryMatch(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "notes.txt"), []byte("pinned-only-content"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Context.Engine = "yarn"
	cfg.Context.Yarn.Pins = "always"
	builder := NewBuilder(cwd, cfg, tools.NewRegistry())
	if _, err := builder.Tray.Pin("@notes.txt"); err != nil {
		t.Fatal(err)
	}
	snapshot := builder.Build("unrelated query")
	if !strings.Contains(snapshot.Render(), "pinned-only-content") {
		t.Fatalf("expected pin included, got:\n%s", snapshot.Render())
	}
}

func TestYarnMentionAlwaysIncludedWithoutQueryMatch(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "target.txt"), []byte("mentioned-only-content"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Context.Engine = "yarn"
	cfg.Context.Yarn.Mentions = "always"
	builder := NewBuilder(cwd, cfg, tools.NewRegistry())
	snapshot := builder.Build("unrelated @target.txt")
	if !strings.Contains(snapshot.Render(), "mentioned-only-content") {
		t.Fatalf("expected mention included, got:\n%s", snapshot.Render())
	}
}

func TestYarnPinsOffSkipsPins(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "notes.txt"), []byte("do-not-include-pin"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Context.Engine = "yarn"
	cfg.Context.Yarn.Pins = "off"
	builder := NewBuilder(cwd, cfg, tools.NewRegistry())
	if _, err := builder.Tray.Pin("@notes.txt"); err != nil {
		t.Fatal(err)
	}
	snapshot := builder.Build("notes")
	if strings.Contains(snapshot.Render(), "do-not-include-pin") {
		t.Fatalf("pin should be skipped when pins=off, got:\n%s", snapshot.Render())
	}
}

func TestYarnDryRunDoesNotWriteNodes(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "target.txt"), []byte("dry-run-content"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Context.Engine = "yarn"
	builder := NewBuilder(cwd, cfg, tools.NewRegistry())
	snapshot := builder.BuildWithOptions("inspect @target.txt", BuildOptions{RecordYarn: false})
	if !strings.Contains(snapshot.Render(), "dry-run-content") {
		t.Fatalf("expected dry-run mention content, got:\n%s", snapshot.Render())
	}
	if _, err := os.Stat(filepath.Join(cwd, ".forge", "yarn", "nodes.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote YARN nodes, stat err=%v", err)
	}
}

func yarnNode(kind, path, content string) yarn.Node {
	return yarn.Node{Kind: kind, Path: path, Summary: kind + " " + path, Content: content}
}

func TestPinnedFileIncludedInContext(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "notes.txt"), []byte("pinned context"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Context.Engine = "simple"

	builder := NewBuilder(cwd, cfg, tools.NewRegistry())
	if _, err := builder.Tray.Pin("@notes.txt"); err != nil {
		t.Fatal(err)
	}
	snapshot := builder.Build("hello")

	rendered := snapshot.Render()
	if !strings.Contains(rendered, "[pinned] notes.txt") {
		t.Fatalf("expected pinned file in context, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "pinned context") {
		t.Fatalf("expected pinned content in context, got:\n%s", rendered)
	}
}

func TestBuildIncludesInstalledAgentSkills(t *testing.T) {
	cwd := t.TempDir()
	skillDir := filepath.Join(cwd, ".agents", "skills", "frontend-design")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: frontend-design\ndescription: Frontend work\n---\nDesign guidance from installed skill."
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Context.Engine = "simple"

	builder := NewBuilder(cwd, cfg, tools.NewRegistry())
	builder.Skills = skills.NewManager(cwd, skills.Options{})
	snapshot := builder.Build("use frontend guidance")

	rendered := snapshot.Render()
	if !strings.Contains(rendered, "Design guidance from installed skill.") {
		t.Fatalf("expected installed skill in context, got:\n%s", rendered)
	}
}
