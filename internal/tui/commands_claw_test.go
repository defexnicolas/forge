package tui

import (
	"strings"
	"testing"

	"forge/internal/claw"
	"forge/internal/config"
	"forge/internal/llm"
	"forge/internal/tools"
)

func TestClawStatusReportsActiveForgeModel(t *testing.T) {
	t.Setenv("FORGE_GLOBAL_HOME", t.TempDir())

	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	cfg.Models["chat"] = "hub-chat"

	provider := &tuiFakeProvider{
		models: []llm.ModelInfo{
			{ID: "hub-chat", LoadedContextLength: 32000, MaxContextLength: 65536},
		},
	}
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	providers := llm.NewRegistry()
	providers.Register(provider)
	clawSvc, err := claw.Open(cfg, providers, registry)
	if err != nil {
		t.Fatalf("claw.Open: %v", err)
	}

	m := newModel(Options{
		CWD:       t.TempDir(),
		Config:    cfg,
		Tools:     registry,
		Providers: providers,
		Claw:      clawSvc,
	})
	t.Cleanup(func() {
		_ = m.agentRuntime.Close()
		_ = clawSvc.Stop()
	})

	out := m.handleClawCommand([]string{"/claw", "status"})
	if !strings.Contains(out, "hub-chat") {
		t.Fatalf("expected /claw status to mention active model, got:\n%s", out)
	}
	if !strings.Contains(out, "fake") {
		t.Fatalf("expected /claw status to mention provider, got:\n%s", out)
	}
}
