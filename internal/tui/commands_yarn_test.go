package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forge/internal/config"
	"forge/internal/llm"
	"forge/internal/session"
	"forge/internal/yarn"
)

func TestBuildYarnGraphHTMLIncludesNodesAndEdges(t *testing.T) {
	nodes := []yarn.Node{
		{ID: "session:one", Kind: "session", Summary: "Session summary", Content: "User asked about plugins", Links: []string{"note:one"}},
		{ID: "note:one", Kind: "note", Summary: "Plugin note", Content: "Plugin details"},
	}
	html, err := buildYarnGraphHTML(nodes)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"YARN Graph", `"id":"session:one"`, `"target":"note:one"`, "Interactive local graph view"} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected HTML to contain %q", want)
		}
	}
}

func TestYarnGraphWritesHTMLAndReturnsPathWhenOpenFails(t *testing.T) {
	cwd := t.TempDir()
	store := yarn.New(cwd)
	if err := store.Upsert(yarn.Node{Kind: "session", Summary: "Session summary", Content: "Content"}); err != nil {
		t.Fatal(err)
	}

	original := openYarnGraphPath
	openYarnGraphPath = func(path string) error { return os.ErrPermission }
	t.Cleanup(func() { openYarnGraphPath = original })

	m := model{
		options: Options{CWD: cwd, Config: config.Defaults()},
		theme:   DefaultTheme(),
	}
	result := stripAnsi(m.yarnGraph())
	graphPath := filepath.Join(cwd, ".forge", "yarn", "graph.html")
	if !strings.Contains(result, graphPath) {
		t.Fatalf("expected output path in result, got:\n%s", result)
	}
	if _, err := os.Stat(graphPath); err != nil {
		t.Fatalf("expected graph HTML file, stat err=%v", err)
	}
}

func TestCompactSessionOmitsTemperatureOverride(t *testing.T) {
	cwd := t.TempDir()
	store, err := session.New(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.LogUser("hola"); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendChatTurn("respuesta"); err != nil {
		t.Fatal(err)
	}

	provider := &tuiFakeProvider{responses: []string{`session compact`}}
	providers := llm.NewRegistry()
	providers.Register(provider)

	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	m := model{
		options: Options{CWD: cwd, Config: cfg, Providers: providers, Session: store},
		theme:   DefaultTheme(),
	}

	out := stripAnsi(m.compactSession())
	if !strings.Contains(out, "Compacted session into YARN") {
		t.Fatalf("unexpected compact output:\n%s", out)
	}
	if len(provider.requests) == 0 {
		t.Fatal("expected compactSession to call provider")
	}
	if provider.requests[0].Temperature != nil {
		t.Fatalf("expected compactSession request to omit temperature override, got %#v", *provider.requests[0].Temperature)
	}
}
