package tui

import (
	"context"
	"strings"

	"forge/internal/agent"

	tea "github.com/charmbracelet/bubbletea"
)

// btwEventMsg wraps a single event from a /btw side-call so the TUI update
// loop can render it distinctly (muted, [btw] prefix) without disturbing the
// main agent's streaming state machine.
type btwEventMsg struct {
	event  agent.Event
	events <-chan agent.Event
}

func waitForBtwEvent(events <-chan agent.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-events
		if !ok {
			return btwEventMsg{event: agent.Event{Type: agent.EventDone, Side: true}, events: events}
		}
		return btwEventMsg{event: ev, events: events}
	}
}

func (m *model) handleBtwCommand(question string) string {
	t := m.theme
	if m.agentRuntime == nil {
		return t.ErrorStyle.Render("Agent runtime unavailable.")
	}
	question = strings.TrimSpace(question)
	if question == "" {
		return "Usage: /btw <question>"
	}
	events := m.agentRuntime.RunBtw(context.Background(), question)
	m.btwEvents = events
	m.pendingCommand = waitForBtwEvent(events)
	return t.Muted.Render("[btw] ") + question
}

func (m *model) appendBtwEvent(event agent.Event) {
	t := m.theme
	prefix := t.Muted.Render("[btw] ")
	switch event.Type {
	case agent.EventAssistantDelta:
		if event.Text == "" {
			return
		}
		if m.btwStreaming {
			m.history[len(m.history)-1] += t.Muted.Render(event.Text)
		} else {
			m.btwStreaming = true
			m.history = append(m.history, "    "+prefix+t.Muted.Render(event.Text))
		}
	case agent.EventAssistantText:
		text := strings.TrimSpace(event.Text)
		if text == "" {
			return
		}
		if !m.btwStreaming {
			for _, line := range strings.Split(text, "\n") {
				m.history = append(m.history, "    "+prefix+t.Muted.Render(line))
			}
		}
		m.btwStreaming = false
	case agent.EventError:
		m.btwStreaming = false
		if event.Error != nil {
			m.history = append(m.history, "    "+prefix+t.ErrorStyle.Render(event.Error.Error()))
		}
	case agent.EventDone:
		m.btwStreaming = false
	}
	if m.options.Session != nil && event.Type != agent.EventModelProgress {
		_ = m.options.Session.LogAgentEvent(event)
	}
	m.forceScrollBottom = true
}
