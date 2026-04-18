package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"forge/internal/agent"
	"forge/internal/tools"

	tea "github.com/charmbracelet/bubbletea"
)

type agentEventMsg struct {
	event  agent.Event
	events <-chan agent.Event
}

// streamFlushMsg triggers a coalesced materialization of streaming deltas
// into the viewport. Scheduled by the Update loop whenever an assistant
// delta arrives and no flush is already pending, so per-token tk/s rates
// of 100+ collapse into ~30 renders/sec of work.
type streamFlushMsg struct{}

// streamFlushInterval trades off perceived smoothness against CPU cost.
// 33ms ≈ 30fps — fast enough that characters still appear to "stream" but
// slow enough that Ollama at 150+ tk/s doesn't saturate the event loop.
const streamFlushInterval = 33 * time.Millisecond

func waitForAgentEvent(events <-chan agent.Event) tea.Cmd {
	return func() tea.Msg {
		event, ok := <-events
		if !ok {
			return agentEventMsg{event: agent.Event{Type: agent.EventDone}, events: events}
		}
		return agentEventMsg{event: event, events: events}
	}
}

func scheduleStreamFlush() tea.Cmd {
	return tea.Tick(streamFlushInterval, func(time.Time) tea.Msg {
		return streamFlushMsg{}
	})
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
			m.currentAssistant.WriteString(event.Text)
			indented := strings.ReplaceAll(event.Text, "\n", "\n    ")
			if m.streaming {
				// Hot path: append only to the builders. Writing to
				// strings.Builder is amortized O(1); the previous
				// `m.history[last] += indented` was O(len(turn)) per token
				// which became the dominant cost at Ollama streaming speeds.
				// The viewport picks up the materialized text from the
				// periodic flush in Update (streamFlushMsg).
				m.streamingBuilder.WriteString(indented)
				m.streamingRaw.WriteString(event.Text)
			} else {
				m.streaming = true
				m.streamingStartIdx = len(m.history)
				m.streamingBuilder.Reset()
				m.streamingRaw.Reset()
				m.streamingBuilder.WriteString("    ")
				m.streamingBuilder.WriteString(indented)
				m.streamingRaw.WriteString(event.Text)
				m.history = append(m.history, "")
				// The new streaming line sits at streamingStartIdx, so
				// everything up to that index is a fresh, stable prefix.
				m.prefixDirty = true
			}
		}
	case agent.EventAssistantText:
		m.streaming = false
		m.streamingStartIdx = -1
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
		// Remove only the streamed assistant block that precedes a tool call.
		// The pending streaming builder is exactly what we need to discard —
		// do not flush it out to history before clearing.
		m.streaming = false
		if m.streamingStartIdx >= 0 && m.streamingStartIdx <= len(m.history) {
			m.history = m.history[:m.streamingStartIdx]
		}
		m.streamingStartIdx = -1
		m.streamingBuilder.Reset()
		m.streamingRaw.Reset()
		lastAgentResponse = ""
		m.prefixDirty = true
	case agent.EventToolCall:
		m.streaming = false
		m.streamingStartIdx = -1
		m.modelProgress = nil
		input := strings.TrimSpace(string(event.Input))
		if input == "" {
			input = "{}"
		}
		m.turnToolActivity = append(m.turnToolActivity, turnToolEntry{
			Name:  event.ToolName,
			Input: summarizeToolInput(event.ToolName, event.Input),
		})
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
		if len(m.turnToolActivity) > 0 && m.turnToolActivity[len(m.turnToolActivity)-1].Name == event.ToolName {
			summary := event.Text
			if summary == "" && event.Result != nil {
				summary = event.Result.Summary
			}
			m.turnToolActivity[len(m.turnToolActivity)-1].Result = truncate(strings.TrimSpace(summary), 160)
		}
		if m.lastToolCollapsed {
			// Result is part of a folded pair — swallow it.
			break
		}
		summary := event.Text
		if summary == "" && event.Result != nil {
			summary = event.Result.Summary
		}
		lines := wrapToolResult(summary, event.ToolName, m.viewport.Width)
		if len(lines) == 0 {
			lines = []string{""}
		}
		m.history = append(m.history, "      "+t.Muted.Render("-> ")+t.ToolResult.Render(event.ToolName+": "+lines[0]))
		for _, line := range lines[1:] {
			m.history = append(m.history, "         "+t.ToolResult.Render(line))
		}
		// Auto-show plan panel when plan/checklist tools produce state-changing
		// results. Explorer mode keeps the full viewport width for analysis.
		if m.agentRuntime == nil || m.agentRuntime.Mode != "explore" {
			if event.ToolName == "todo_write" || strings.HasPrefix(event.ToolName, "task_") || event.ToolName == "plan_write" {
				if !m.showPlan {
					m.showPlan = true
					m.recalcLayout()
				}
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
		m.streamingStartIdx = -1
		m.modelProgress = nil
		m.toolUsesInTurn = 0
		m.collapsedToolLineIdx = -1
		m.lastToolCollapsed = false
		exploreHandoff := ""
		if m.agentRuntime != nil && m.agentRuntime.Mode == "explore" {
			exploreHandoff = buildExploreHandoff(m.turnUserInput, m.turnToolActivity, m.currentAssistant.String())
		}
		// Persist a clean Q&A transcript line for the session's chat.md.
		if m.options.Session != nil {
			_ = m.options.Session.AppendChatTurn(m.currentAssistant.String())
		}
		m.currentAssistant.Reset()
		m.turnToolActivity = nil
		m.turnUserInput = ""
		duration := m.agentRuntime.LastTurnDuration
		tokensIn := m.agentRuntime.LastTurnTokensIn
		tokensOut := m.agentRuntime.LastTurnTokensOut
		tps := m.agentRuntime.LastTurnTokensPerSec
		timing := fmt.Sprintf("%.1fs", duration.Seconds())
		tokens := fmt.Sprintf("~%d in, ~%d out", tokensIn, tokensOut)
		suffix := "  " + timing + " | " + tokens
		if tps > 0 {
			suffix += fmt.Sprintf(" | %.1f tk/s", tps)
		}
		m.history = append(m.history, "")
		m.history = append(m.history, "    "+t.IndicatorDone.Render("* ")+t.DoneStyle.Render("turn complete")+t.Muted.Render(suffix))
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

// summarizeToolInput extracts the one-or-two high-signal fields from a
// tool_call input JSON so the explore→plan handoff can carry "what did the
// explorer actually do" (which paths, which queries) without dragging the
// whole payload. Returns a short "path=..., query=..." style string.
func summarizeToolInput(toolName string, input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var decoded map[string]any
	if err := json.Unmarshal(input, &decoded); err != nil {
		return ""
	}
	keys := toolInputSignalKeys(toolName)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		v, ok := decoded[key]
		if !ok {
			continue
		}
		switch s := v.(type) {
		case string:
			if s == "" {
				continue
			}
			parts = append(parts, key+"="+truncate(s, 80))
		default:
			if data, err := json.Marshal(v); err == nil && string(data) != "\"\"" {
				parts = append(parts, key+"="+truncate(string(data), 80))
			}
		}
	}
	return strings.Join(parts, ", ")
}

func toolInputSignalKeys(toolName string) []string {
	switch toolName {
	case "read_file":
		return []string{"path"}
	case "list_files":
		return []string{"path", "pattern"}
	case "search_text":
		return []string{"query", "path"}
	case "search_files":
		return []string{"pattern", "path"}
	case "git_diff":
		return []string{"path", "staged"}
	case "apply_patch", "edit_file", "write_file":
		return []string{"path"}
	case "run_command", "powershell_command":
		return []string{"command"}
	case "spawn_subagent":
		return []string{"agent", "prompt"}
	case "spawn_subagents":
		return []string{"tasks"}
	case "ask_user":
		return []string{"question"}
	case "plan_write", "plan_get":
		return []string{"summary"}
	case "todo_write":
		return []string{"items"}
	case "task_create", "task_update", "task_get", "task_list":
		return []string{"id", "title", "status"}
	default:
		return []string{"path", "query", "pattern"}
	}
}

// buildExploreHandoff formats the explore turn's activity as a structured
// summary for plan mode. Includes the user's question, the tools the
// explorer exercised (with the files/queries they targeted), and the final
// assistant text. Without this, plan mode only sees the final text and
// loses the "where did we look" signal that explorers typically produce.
func buildExploreHandoff(userInput string, activity []turnToolEntry, finalText string) string {
	final := strings.TrimSpace(finalText)
	if len(activity) == 0 && strings.TrimSpace(userInput) == "" {
		return final
	}
	var b strings.Builder
	if q := strings.TrimSpace(userInput); q != "" {
		b.WriteString("QUESTION:\n")
		b.WriteString(q)
		b.WriteString("\n\n")
	}
	if len(activity) > 0 {
		grouped := groupToolActivity(activity)
		b.WriteString("EXPLORER ACTIVITY:\n")
		for _, bucket := range grouped {
			if bucket.label == "" || len(bucket.entries) == 0 {
				continue
			}
			fmt.Fprintf(&b, "- %s:\n", bucket.label)
			for _, e := range bucket.entries {
				line := "    - " + e.Input
				if e.Result != "" {
					line += "  →  " + e.Result
				}
				b.WriteString(line)
				b.WriteByte('\n')
			}
		}
		b.WriteByte('\n')
	}
	if final != "" {
		b.WriteString("FINDINGS:\n")
		b.WriteString(final)
	}
	return strings.TrimSpace(b.String())
}

type toolActivityBucket struct {
	label   string
	entries []turnToolEntry
}

func groupToolActivity(activity []turnToolEntry) []toolActivityBucket {
	order := []string{"read_file", "list_files", "search_text", "search_files", "git_diff", "git_status", "spawn_subagent", "spawn_subagents", "run_command", "powershell_command"}
	labels := map[string]string{
		"read_file":          "Files read",
		"list_files":         "Directories listed",
		"search_text":        "Text searches",
		"search_files":       "File-name searches",
		"git_diff":           "Diffs inspected",
		"git_status":         "Repo status checks",
		"spawn_subagent":     "Subagent runs",
		"spawn_subagents":    "Subagent batches",
		"run_command":        "Commands executed",
		"powershell_command": "Commands executed",
	}
	byName := map[string][]turnToolEntry{}
	for _, e := range activity {
		byName[e.Name] = append(byName[e.Name], e)
	}
	buckets := make([]toolActivityBucket, 0, len(order)+1)
	for _, name := range order {
		if entries, ok := byName[name]; ok {
			buckets = append(buckets, toolActivityBucket{label: labels[name], entries: entries})
			delete(byName, name)
		}
	}
	var others []turnToolEntry
	for _, entries := range byName {
		others = append(others, entries...)
	}
	if len(others) > 0 {
		buckets = append(buckets, toolActivityBucket{label: "Other tools", entries: others})
	}
	return buckets
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

func wrapToolResult(summary, toolName string, viewportWidth int) []string {
	if strings.TrimSpace(summary) == "" {
		return nil
	}
	firstWidth := max(20, viewportWidth-len(toolName)-16)
	nextWidth := max(20, viewportWidth-12)
	var out []string
	for i, line := range strings.Split(summary, "\n") {
		width := nextWidth
		if i == 0 {
			width = firstWidth
		}
		out = append(out, wrapPlainLine(line, width)...)
	}
	return out
}

func wrapPlainLine(line string, width int) []string {
	if width <= 0 || len(line) <= width {
		return []string{line}
	}
	var out []string
	for len(line) > width {
		cut := width
		if idx := strings.LastIndexAny(line[:width], " \t"); idx > width/2 {
			cut = idx
		}
		out = append(out, strings.TrimRight(line[:cut], " \t"))
		line = strings.TrimLeft(line[cut:], " \t")
	}
	out = append(out, line)
	return out
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
