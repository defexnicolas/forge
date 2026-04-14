package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"forge/internal/agent"
	"forge/internal/tools"

	tea "github.com/charmbracelet/bubbletea"
)

type agentEventMsg struct {
	event  agent.Event
	events <-chan agent.Event
}

func waitForAgentEvent(events <-chan agent.Event) tea.Cmd {
	return func() tea.Msg {
		event, ok := <-events
		if !ok {
			return agentEventMsg{event: agent.Event{Type: agent.EventDone}, events: events}
		}
		return agentEventMsg{event: event, events: events}
	}
}

func (m *model) appendAgentEvent(event agent.Event) {
	t := m.theme
	if m.options.Session != nil {
		_ = m.options.Session.LogAgentEvent(event)
	}
	switch event.Type {
	case agent.EventAssistantDelta:
		if event.Text != "" {
			lastAgentResponse += event.Text
			if m.streaming {
				m.history[len(m.history)-1] += event.Text
			} else {
				m.streaming = true
				lastAgentResponse = event.Text
				m.history = append(m.history, "    "+event.Text)
			}
		}
	case agent.EventAssistantText:
		m.streaming = false
		if text := strings.TrimSpace(event.Text); text != "" {
			text = m.formatThinking(text)
			indented := ""
			for _, line := range strings.Split(text, "\n") {
				indented += "    " + line + "\n"
			}
			m.history = append(m.history, strings.TrimRight(indented, "\n"))
		}
	case agent.EventClearStreaming:
		// Remove streamed text lines that precede a tool call.
		m.streaming = false
		for len(m.history) > 0 {
			last := m.history[len(m.history)-1]
			if strings.HasPrefix(last, "    ") && !strings.Contains(last, "* ") && !strings.Contains(last, "-> ") {
				m.history = m.history[:len(m.history)-1]
			} else {
				break
			}
		}
		lastAgentResponse = ""
	case agent.EventToolCall:
		m.streaming = false
		input := strings.TrimSpace(string(event.Input))
		if input == "" {
			input = "{}"
		}
		m.history = append(m.history, "")
		m.history = append(m.history, "    "+t.IndicatorTool.Render("* ")+t.ToolCallStyle.Render(event.ToolName)+" "+t.Muted.Render(truncate(input, 100)))
	case agent.EventToolResult:
		summary := event.Text
		if summary == "" && event.Result != nil {
			summary = event.Result.Summary
		}
		m.history = append(m.history, "      "+t.Muted.Render("-> ")+t.ToolResult.Render(event.ToolName+": "+truncate(summary, 160)))
		// Auto-show plan panel when todo_write or task tools produce results.
		if event.ToolName == "todo_write" || strings.HasPrefix(event.ToolName, "task_") {
			if !m.showPlan {
				m.showPlan = true
				m.recalcLayout()
			}
		}
	case agent.EventError:
		if event.Error != nil {
			m.history = append(m.history, "    "+t.IndicatorError.Render("* ")+t.ErrorStyle.Render(event.Error.Error()))
		}
	case agent.EventAskUser:
		m.streaming = false
		m.pendingAskUser = event.AskUser
		if event.AskUser != nil {
			m.history = append(m.history, "")
			m.history = append(m.history, "    "+t.ApprovalStyle.Render("? ")+t.StatusValue.Render(event.AskUser.Question))
			m.history = append(m.history, "")
			m.forceScrollBottom = true
		}
	case agent.EventApproval:
		m.streaming = false
		m.pendingApproval = event.Approval
		if event.Approval == nil {
			m.history = append(m.history, t.IndicatorError.Render("* ")+t.ApprovalStyle.Render("approval required"))
			return
		}
		added, removed := CountDiffChanges(event.Approval.Diff)
		m.history = append(m.history, t.FormatPatchSummary(event.Approval.ToolName, added, removed, event.Approval.Diff))
		m.history = append(m.history, "")
		// Approve/reject hint is shown in the status bar, not inline.
		m.forceScrollBottom = true
	case agent.EventDone:
		m.streaming = false
		duration := m.agentRuntime.LastTurnDuration
		tokensIn := m.agentRuntime.LastTurnTokensIn
		tokensOut := m.agentRuntime.LastTurnTokensOut
		timing := fmt.Sprintf("%.1fs", duration.Seconds())
		tokens := fmt.Sprintf("~%d in, ~%d out", tokensIn, tokensOut)
		m.history = append(m.history, "")
		m.history = append(m.history, "    "+t.IndicatorDone.Render("* ")+t.DoneStyle.Render("turn complete")+t.Muted.Render("  "+timing+" | "+tokens))
		m.history = append(m.history, t.SeparatorLine(m.width-4))
		m.history = append(m.history, "")
		m.forceScrollBottom = true
	}
}

func (m model) formatThinking(text string) string {
	t := m.theme
	thinkOpen := "<think>"
	thinkClose := "</think>"
	start := strings.Index(text, thinkOpen)
	if start < 0 {
		return text
	}
	end := strings.Index(text, thinkClose)
	if end < 0 {
		if m.thinkEnabled {
			return text
		}
		return strings.TrimSpace(text[:start])
	}
	thinking := text[start+len(thinkOpen) : end]
	after := strings.TrimSpace(text[end+len(thinkClose):])
	before := strings.TrimSpace(text[:start])

	if !m.thinkEnabled {
		result := before
		if after != "" {
			if result != "" {
				result += "\n"
			}
			result += after
		}
		return result
	}
	var b strings.Builder
	if before != "" {
		b.WriteString(before + "\n")
	}
	b.WriteString(t.Muted.Render("+-- thinking ----------------") + "\n")
	for _, line := range strings.Split(strings.TrimSpace(thinking), "\n") {
		b.WriteString(t.Muted.Render("| "+line) + "\n")
	}
	b.WriteString(t.Muted.Render("+----------------------------"))
	if after != "" {
		b.WriteString("\n" + after)
	}
	return b.String()
}

func truncate(s string, limit int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > limit {
		return s[:limit] + "..."
	}
	return s
}

func summarizeContent(blocks []tools.ContentBlock) string {
	data, err := json.MarshalIndent(blocks, "", "  ")
	if err != nil {
		return ""
	}
	text := string(data)
	if len(text) > 2000 {
		return text[:2000] + "\n[truncated]"
	}
	return text
}
