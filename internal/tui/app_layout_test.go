package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"forge/internal/agent"
	"forge/internal/tools"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestAppLayoutFitsTerminalHeight(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, model) model
	}{
		{
			name: "normal",
			setup: func(t *testing.T, m model) model {
				return m
			},
		},
		{
			name: "suggestions",
			setup: func(t *testing.T, m model) model {
				m.suggestions = []string{"/help", "/provider", "/permissions", "/context", "/session", "/skills", "/plugins", "/really-long-command-to-wrap"}
				return m
			},
		},
		{
			name: "search",
			setup: func(t *testing.T, m model) model {
				m.searching = true
				m.searchMode = newSearchMode(m.theme)
				m.searchMode.active = true
				return m
			},
		},
		{
			name: "plan panel",
			setup: func(t *testing.T, m model) model {
				if _, err := m.agentRuntime.Tasks.Create("keep the latest model response visible", ""); err != nil {
					t.Fatal(err)
				}
				m.showPlan = true
				return m
			},
		},
		{
			name: "multiline input",
			setup: func(t *testing.T, m model) model {
				m.input.SetValue("first line\nsecond line\nthird line")
				return m
			},
		},
		{
			name: "ask user modal",
			setup: func(t *testing.T, m model) model {
				m.activeForm = formAskUser
				m.askUserForm = newAskUserForm(&agent.AskUserRequest{
					Question: "Which direction should the plan take?",
					Options:  []string{"Small patch", "Broader refactor", "Investigate first"},
				}, m.theme, m.width, m.height)
				return m
			},
		},
		{
			name: "plan reset confirm",
			setup: func(t *testing.T, m model) model {
				m.activeForm = formConfirmPlanReset
				m.confirmPlanReset = newConfirmFormWithDefault("A prior plan exists. Clear it and start fresh?", m.theme, false)
				return m
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const terminalHeight = 24
			m := newSizedLayoutModel(t, 96, terminalHeight)
			m.history = append(m.history, layoutHistoryLines(40)...)
			m = tt.setup(t, m)
			m.refresh()

			if got := lipgloss.Height(m.View()); got > terminalHeight {
				t.Fatalf("view height = %d, want <= %d\n%s", got, terminalHeight, stripAnsi(m.View()))
			}
		})
	}
}

func TestAppLayoutKeepsBlankLineAboveInput(t *testing.T) {
	m := newSizedLayoutModel(t, 96, 24)
	m.history = append(m.history, layoutHistoryLines(20)...)
	m.refresh()

	view := stripAnsi(m.View())
	want := stripAnsi(m.viewportView()) + "\n\n" + stripAnsi(m.inputBoxView())
	if !strings.Contains(view, want) {
		t.Fatalf("view does not keep a blank line above input\nwant fragment:\n%s\n\nview:\n%s", want, view)
	}
}

func TestAppLayoutShowsModelProgressAboveInput(t *testing.T) {
	m := newSizedLayoutModel(t, 96, 24)
	m.history = append(m.history, layoutHistoryLines(20)...)
	m.agentRunning = true
	m.modelProgress = &agent.ModelProgress{
		Phase:           "streaming",
		Step:            1,
		InputTokens:     12000,
		OutputTokens:    128,
		TotalTokens:     12128,
		TokensPerSecond: 24.5,
		Elapsed:         1500 * time.Millisecond,
	}
	m.refresh()

	view := stripAnsi(m.View())
	want := stripAnsi(m.viewportView()) + "\n  * streaming | step:1 | in:12.0k out:128 total:12.1k | 24.5 tk/s | 1.5s\n" + stripAnsi(m.inputBoxView())
	if !strings.Contains(view, want) {
		t.Fatalf("view does not place model progress above input\nwant fragment:\n%s\n\nview:\n%s", want, view)
	}
}

func TestTextareaStartsSingleLine(t *testing.T) {
	m := newSizedLayoutModel(t, 96, 24)
	if got := m.input.Height(); got != inputMinLines {
		t.Fatalf("initial input height = %d, want %d", got, inputMinLines)
	}
}

func TestTextareaGrowsAndCaps(t *testing.T) {
	m := newSizedLayoutModel(t, 96, 40)

	// Three lines grows to 3.
	m.input.SetValue("line1\nline2\nline3")
	m.recalcLayout()
	if got := m.input.Height(); got != 3 {
		t.Fatalf("three-line input height = %d, want 3", got)
	}

	// Far more than the cap clamps to inputMaxLines.
	m.input.SetValue(strings.Repeat("x\n", 20))
	m.recalcLayout()
	if got := m.input.Height(); got != inputMaxLines {
		t.Fatalf("oversized input height = %d, want %d (cap)", got, inputMaxLines)
	}
}

func TestScrollUpDisablesStickyBottom(t *testing.T) {
	m := newSizedLayoutModel(t, 96, 24)
	m.history = append(m.history, layoutHistoryLines(200)...)
	m.refresh()
	if !m.stickyBottom {
		t.Fatalf("expected stickyBottom true after initial refresh")
	}

	// Simulate user pressing PgUp on an empty input.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	m = updated.(model)
	if m.stickyBottom {
		t.Fatalf("stickyBottom should be false after scrolling up")
	}

	// Returning to the bottom via PgDown should re-enable sticky.
	for i := 0; i < 50 && !m.viewport.AtBottom(); i++ {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
		m = updated.(model)
	}
	if !m.stickyBottom {
		t.Fatalf("stickyBottom should be re-enabled after scrolling back to bottom")
	}
}

func TestToolResultSurvivesClearStreaming(t *testing.T) {
	m := newSizedLayoutModel(t, 80, 24)
	m.appendAgentEvent(agent.Event{
		Type:     agent.EventToolResult,
		ToolName: "read_file",
		Result:   &tools.Result{Summary: "first result\nimportant continuation"},
	})
	m.appendAgentEvent(agent.Event{Type: agent.EventAssistantDelta, Text: "draft before tool"})
	m.appendAgentEvent(agent.Event{Type: agent.EventClearStreaming})

	history := stripAnsi(strings.Join(m.history, "\n"))
	if !strings.Contains(history, "first result") || !strings.Contains(history, "important continuation") {
		t.Fatalf("tool result was removed by clear streaming:\n%s", history)
	}
	if strings.Contains(history, "draft before tool") {
		t.Fatalf("streamed draft should have been removed:\n%s", history)
	}
}

func TestToolResultWrapsToViewport(t *testing.T) {
	m := newSizedLayoutModel(t, 56, 24)
	long := "this is a very long diagnostic line that should wrap across multiple rows instead of disappearing off the right edge"
	m.appendAgentEvent(agent.Event{
		Type:     agent.EventToolResult,
		ToolName: "read_file",
		Result:   &tools.Result{Summary: long},
	})

	history := stripAnsi(strings.Join(m.history, "\n"))
	if !strings.Contains(history, "this is a very long") ||
		!strings.Contains(history, "instead of disappearing") {
		t.Fatalf("wrapped content missing from history:\n%s", history)
	}
	if got := strings.Count(history, "         "); got < 1 {
		t.Fatalf("expected wrapped continuation lines, got:\n%s", history)
	}
}

func TestExplorerDoesNotAutoShowPlanPanel(t *testing.T) {
	m := newSizedLayoutModel(t, 96, 24)
	if err := m.agentRuntime.SetMode("explore"); err != nil {
		t.Fatal(err)
	}
	m.appendAgentEvent(agent.Event{
		Type:     agent.EventToolResult,
		ToolName: "plan_write",
		Result:   &tools.Result{Summary: "updated plan"},
	})
	if m.showPlan {
		t.Fatal("explorer mode should not auto-show the plan panel")
	}
}

func TestSwitchingToExploreHidesPlanPanel(t *testing.T) {
	m := newSizedLayoutModel(t, 96, 24)
	m.showPlan = true
	m.recalcLayout()
	narrowWidth := m.viewport.Width
	_ = m.setMode("explore", "")
	if m.showPlan {
		t.Fatal("explore mode should hide an already-open plan panel")
	}
	if m.viewport.Width <= narrowWidth {
		t.Fatalf("expected explore viewport to widen, got before=%d after=%d", narrowWidth, m.viewport.Width)
	}
}

func TestPlanModeStillAutoShowsPlanPanel(t *testing.T) {
	m := newSizedLayoutModel(t, 96, 24)
	if err := m.agentRuntime.SetMode("plan"); err != nil {
		t.Fatal(err)
	}
	m.appendAgentEvent(agent.Event{
		Type:     agent.EventToolResult,
		ToolName: "todo_write",
		Result:   &tools.Result{Summary: "updated checklist"},
	})
	if !m.showPlan {
		t.Fatal("plan mode should auto-show the plan panel after checklist updates")
	}
}

func newSizedLayoutModel(t *testing.T, width, height int) model {
	t.Helper()
	m := newModel(Options{CWD: t.TempDir()})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	sized, ok := updated.(model)
	if !ok {
		t.Fatalf("Update returned %T, want tui.model", updated)
	}
	if sized.agentRuntime != nil {
		t.Cleanup(func() {
			_ = sized.agentRuntime.Close()
		})
	}
	return sized
}

func layoutHistoryLines(count int) []string {
	lines := make([]string, count)
	for i := range lines {
		lines[i] = fmt.Sprintf("    model response line %02d %s", i+1, strings.Repeat("x", 20))
	}
	return lines
}
