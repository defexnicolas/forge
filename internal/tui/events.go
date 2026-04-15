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
	if m.options.Session != nil && event.Type != agent.EventModelProgress {
		_ = m.options.Session.LogAgentEvent(event)
	}
	switch event.Type {
	case agent.EventModelProgress:
		if event.Progress != nil {
			progress := *event.Progress
			m.modelProgress = &progress
		}
	case agent.EventAssistantDelta:
		if event.Text != "" {
			lastAgentResponse += event.Text
			m.currentAssistant.WriteString(event.Text)
			// Preserve indent on every wrapped/newline'd line.
			indented := strings.ReplaceAll(event.Text, "\n", "\n    ")
			if m.streaming {
				m.history[len(m.history)-1] += indented
			} else {
				m.streaming = true
				lastAgentResponse = event.Text
				m.history = append(m.history, "    "+indented)
			}
		}
	case agent.EventAssistantText:
		m.streaming = false
		// Assistant is speaking again — any new tool group restarts from zero.
		m.toolUsesInTurn = 0
		m.collapsedToolLineIdx = -1
		m.lastToolCollapsed = false
		if text := strings.TrimSpace(event.Text); text != "" {
			m.currentAssistant.WriteString(text)
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
		m.modelProgress = nil
		input := strings.TrimSpace(string(event.Input))
		if input == "" {
			input = "{}"
		}
		m.toolUsesInTurn++
		// Show the first 2 tool uses in full; fold the rest into a counter.
		if m.toolUsesInTurn <= 2 {
			m.lastToolCollapsed = false
			m.history = append(m.history, "")
			m.history = append(m.history, "    "+t.IndicatorTool.Render("* ")+t.ToolCallStyle.Render(event.ToolName)+" "+t.Muted.Render(truncate(input, 100)))
		} else {
			m.lastToolCollapsed = true
			collapsed := m.toolUsesInTurn - 2
			line := "    " + t.Muted.Render(fmt.Sprintf("+%d more tool uses", collapsed))
			if m.collapsedToolLineIdx >= 0 && m.collapsedToolLineIdx < len(m.history) {
				m.history[m.collapsedToolLineIdx] = line
			} else {
				m.history = append(m.history, line)
				m.collapsedToolLineIdx = len(m.history) - 1
			}
		}
	case agent.EventToolResult:
		if m.lastToolCollapsed {
			// Result is part of a folded pair — swallow it.
			break
		}
		summary := event.Text
		if summary == "" && event.Result != nil {
			summary = event.Result.Summary
		}
		// Multi-line summaries (e.g. todo_write plan list) keep indent on every line.
		if strings.Contains(summary, "\n") {
			lines := strings.Split(summary, "\n")
			m.history = append(m.history, "      "+t.Muted.Render("-> ")+t.ToolResult.Render(event.ToolName+": "+lines[0]))
			for _, line := range lines[1:] {
				m.history = append(m.history, "         "+t.ToolResult.Render(line))
			}
		} else {
			m.history = append(m.history, "      "+t.Muted.Render("-> ")+t.ToolResult.Render(event.ToolName+": "+truncate(summary, 160)))
		}
		// Auto-show plan panel when plan/checklist tools produce results.
		if event.ToolName == "todo_write" || strings.HasPrefix(event.ToolName, "task_") || strings.HasPrefix(event.ToolName, "plan_") {
			if !m.showPlan {
				m.showPlan = true
				m.recalcLayout()
			}
		}
	case agent.EventError:
		m.modelProgress = nil
		if event.Error != nil {
			m.history = append(m.history, "    "+t.IndicatorError.Render("* ")+t.ErrorStyle.Render(event.Error.Error()))
		}
	case agent.EventAskUser:
		m.streaming = false
		m.modelProgress = nil
		m.pendingAskUser = event.AskUser
		if event.AskUser != nil {
			first := event.AskUser.Question
			if first == "" && len(event.AskUser.Questions) > 0 {
				first = event.AskUser.Questions[0]
			}
			m.history = append(m.history, "")
			m.history = append(m.history, "    "+t.ApprovalStyle.Render("? ")+t.Muted.Render(truncate(first, 100)))
			m.activeForm = formAskUser
			m.askUserForm = newAskUserForm(event.AskUser, t, m.width, m.height)
			m.forceScrollBottom = true
		}
	case agent.EventApproval:
		m.streaming = false
		m.modelProgress = nil
		m.pendingApproval = event.Approval
		if event.Approval == nil {
			m.history = append(m.history, t.IndicatorError.Render("* ")+t.ApprovalStyle.Render("approval required"))
			return
		}
		// Pop a modal over the chat. /approve and /reject still work via the
		// command palette as a keyboard fallback, but the common path is the
		// form overlay.
		m.activeForm = formApproval
		m.approvalForm = newApprovalForm(event.Approval, t, m.width, m.height)
		m.forceScrollBottom = true
	case agent.EventDone:
		m.streaming = false
		m.modelProgress = nil
		m.toolUsesInTurn = 0
		m.collapsedToolLineIdx = -1
		m.lastToolCollapsed = false
		exploreHandoff := ""
		if m.agentRuntime != nil && m.agentRuntime.Mode == "explore" {
			exploreHandoff = strings.TrimSpace(m.currentAssistant.String())
		}
		// Persist a clean Q&A transcript line for the session's chat.md.
		if m.options.Session != nil {
			_ = m.options.Session.AppendChatTurn(m.currentAssistant.String())
		}
		m.currentAssistant.Reset()
		duration := m.agentRuntime.LastTurnDuration
		tokensIn := m.agentRuntime.LastTurnTokensIn
		tokensOut := m.agentRuntime.LastTurnTokensOut
		timing := fmt.Sprintf("%.1fs", duration.Seconds())
		tokens := fmt.Sprintf("~%d in, ~%d out", tokensIn, tokensOut)
		m.history = append(m.history, "")
		m.history = append(m.history, "    "+t.IndicatorDone.Render("* ")+t.DoneStyle.Render("turn complete")+t.Muted.Render("  "+timing+" | "+tokens))
		m.history = append(m.history, t.SeparatorLine(m.width-4))
		m.history = append(m.history, "")
		if exploreHandoff != "" {
			m.pendingExplorerHandoff = exploreHandoff
			m.activeForm = formConfirmExplorerPlan
			m.confirmExplorerPlan = newConfirmForm("Pass explorer findings to Plan mode?", m.theme)
			m.history = append(m.history, t.Muted.Render("Explorer finished. Confirm to send these findings to Plan mode."))
		} else if m.shouldOfferPlanExecution() {
			m.pendingExecuteLine = "Execute the approved plan."
			m.activeForm = formConfirmExecute
			m.confirmExecute = newConfirmFormWithDefault("Execute this plan in Build mode?", m.theme, false)
			m.history = append(m.history, t.Muted.Render("Plan finished. Press Y to execute it in Build mode, or Enter/Esc to leave it pending."))
		}
		m.forceScrollBottom = true
	}
}

func (m *model) shouldOfferPlanExecution() bool {
	if m == nil || m.agentRuntime == nil || m.agentRuntime.Mode != "plan" || m.agentRuntime.Tasks == nil {
		return false
	}
	list, err := m.agentRuntime.Tasks.List()
	if err != nil || len(list) == 0 {
		return false
	}
	for _, task := range list {
		if task.Status == "pending" || task.Status == "in_progress" || task.Status == "" {
			return true
		}
	}
	return false
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
