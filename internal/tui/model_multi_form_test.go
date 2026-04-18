package tui

import (
	"strings"
	"testing"

	"forge/internal/config"
	"forge/internal/llm"
	"forge/internal/session"
	"forge/internal/tools"

	tea "github.com/charmbracelet/bubbletea"
)

func TestModelMultiFormAssignsAgentRoleModels(t *testing.T) {
	provider := &tuiFakeProvider{models: []llm.ModelInfo{
		{ID: "explore-model", LoadedContextLength: 16384},
		{ID: "plan-model", LoadedContextLength: 32768},
		{ID: "build-model", LoadedContextLength: 65536},
	}}
	m := newModelMultiTestModel(t, provider)

	if out := m.handleCommand("/model-multi"); out == "" || m.activeForm != formModelMulti {
		t.Fatalf("expected model-multi form, out=%q active=%v", out, m.activeForm)
	}

	m = updateModelMultiTest(t, m, tea.KeyMsg{Type: tea.KeyEnter}) // strategy: single

	// Role 0 (EXPLORER): no prior selections and no models reported as
	// State="loaded", so the reuse step is skipped — straight to the
	// model picker.
	m = updateModelMultiTest(t, m, tea.KeyMsg{Type: tea.KeyEnter}) // explorer: first model
	m = updateModelMultiTest(t, m, tea.KeyMsg{Type: tea.KeyEnter}) // keep current

	// Role 1 (PLAN): the reuse step now appears with "explore-model (from
	// EXPLORER)" + "Pick a different model". We want the second model, so
	// Down -> Pick a different model -> then navigate in the model form.
	m = updateModelMultiTest(t, m, tea.KeyMsg{Type: tea.KeyDown})  // cursor -> "Pick different"
	m = updateModelMultiTest(t, m, tea.KeyMsg{Type: tea.KeyEnter}) // enter model picker
	m = updateModelMultiTest(t, m, tea.KeyMsg{Type: tea.KeyDown})
	m = updateModelMultiTest(t, m, tea.KeyMsg{Type: tea.KeyEnter}) // plan: second model
	m = updateModelMultiTest(t, m, tea.KeyMsg{Type: tea.KeyEnter}) // keep current

	// Role 2 (BUILDER): reuse step now has 2 options + "Pick different".
	// Same pattern: skip to picker, pick third model.
	m = updateModelMultiTest(t, m, tea.KeyMsg{Type: tea.KeyDown})
	m = updateModelMultiTest(t, m, tea.KeyMsg{Type: tea.KeyDown})
	m = updateModelMultiTest(t, m, tea.KeyMsg{Type: tea.KeyEnter}) // into picker
	m = updateModelMultiTest(t, m, tea.KeyMsg{Type: tea.KeyDown})
	m = updateModelMultiTest(t, m, tea.KeyMsg{Type: tea.KeyDown})
	m = updateModelMultiTest(t, m, tea.KeyMsg{Type: tea.KeyEnter}) // builder: third model
	m = updateModelMultiTest(t, m, tea.KeyMsg{Type: tea.KeyEnter}) // keep current

	// Role 3 (REVIEWER): reuse explore-model (first option) — this
	// validates the reuse-by-session path without going through the
	// picker or triggering another load.
	m = updateModelMultiTest(t, m, tea.KeyMsg{Type: tea.KeyEnter}) // reuse first option (explore-model)

	// Role 4 (SUMMARIZER): same — reuse explore-model.
	m = updateModelMultiTest(t, m, tea.KeyMsg{Type: tea.KeyEnter}) // reuse first option (explore-model)

	if m.activeForm != formNone {
		t.Fatalf("activeForm = %v, want formNone", m.activeForm)
	}
	if !m.options.Config.ModelLoading.Enabled || m.options.Config.ModelLoading.Strategy != "single" {
		t.Fatalf("unexpected model loading config: %#v", m.options.Config.ModelLoading)
	}
	if got := m.options.Config.Models["explorer"]; got != "explore-model" {
		t.Fatalf("explorer model = %q", got)
	}
	if got := m.options.Config.Models["planner"]; got != "plan-model" {
		t.Fatalf("planner model = %q", got)
	}
	if got := m.options.Config.Models["editor"]; got != "build-model" {
		t.Fatalf("editor model = %q", got)
	}
	if got := m.options.Config.Models["reviewer"]; got != "explore-model" {
		t.Fatalf("reviewer model = %q", got)
	}
	if got := m.options.Config.Models["summarizer"]; got != "explore-model" {
		t.Fatalf("summarizer model = %q", got)
	}
}

func TestModelMultiOffDisablesMultiModelRouting(t *testing.T) {
	m := newModelMultiTestModel(t, &tuiFakeProvider{})
	m.options.Config.ModelLoading.Enabled = true
	m.agentRuntime.Config = m.options.Config

	out := stripAnsi(m.handleCommand("/model-multi off"))
	if !strings.Contains(out, "Model multi: OFF") {
		t.Fatalf("unexpected output: %q", out)
	}
	if m.options.Config.ModelLoading.Enabled {
		t.Fatal("expected TUI config model multi disabled")
	}
	if m.agentRuntime.Config.ModelLoading.Enabled {
		t.Fatal("expected runtime config model multi disabled")
	}
	if m.activeForm != formNone {
		t.Fatalf("activeForm = %v, want formNone", m.activeForm)
	}
}

func newModelMultiTestModel(t *testing.T, provider *tuiFakeProvider) model {
	t.Helper()
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	cwd := t.TempDir()
	store, err := session.New(cwd)
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	providers := llm.NewRegistry()
	providers.Register(provider)
	m := newModel(Options{CWD: cwd, Config: cfg, Tools: registry, Providers: providers, Session: store})
	t.Cleanup(func() {
		_ = m.agentRuntime.Close()
	})
	return m
}

func updateModelMultiTest(t *testing.T, m model, msg tea.Msg) model {
	t.Helper()
	updated, cmd, handled := m.handleFormUpdate(msg)
	if !handled {
		t.Fatal("expected model-multi form to handle update")
	}
	if cmd != nil {
		msg := cmd()
		updated, cmd, handled = updated.(*model).handleFormUpdate(msg)
		if !handled {
			t.Fatal("expected model-multi form to handle command result")
		}
		if cmd != nil {
			t.Fatal("unexpected chained command")
		}
	}
	ptr, ok := updated.(*model)
	if !ok {
		t.Fatalf("handleFormUpdate returned %T", updated)
	}
	return *ptr
}
