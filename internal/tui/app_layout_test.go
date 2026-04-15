package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"forge/internal/agent"

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
	want := stripAnsi(m.viewport.View()) + "\n\n" + stripAnsi(m.inputBoxView())
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
	want := stripAnsi(m.viewport.View()) + "\n  * streaming | step:1 | in:12.0k out:128 total:12.1k | 24.5 tk/s | 1.5s\n" + stripAnsi(m.inputBoxView())
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
