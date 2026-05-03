package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"forge/internal/claw"
)

func (m *model) handleClawCommand(fields []string) string {
	if m.options.Claw == nil {
		return "Claw service not available."
	}
	if len(fields) == 1 || fields[1] == "status" {
		return m.describeClaw()
	}
	switch fields[1] {
	case "start":
		if err := m.options.Claw.Start(); err != nil {
			return m.theme.ErrorStyle.Render("Claw start failed: " + err.Error())
		}
		return m.theme.Success.Render("Claw heartbeat started.")
	case "stop":
		if err := m.options.Claw.Stop(); err != nil {
			return m.theme.ErrorStyle.Render("Claw stop failed: " + err.Error())
		}
		return m.theme.Muted.Render("Claw heartbeat stopped.")
	case "dream":
		result, err := m.options.Claw.RunDream(context.Background(), "manual")
		if err != nil {
			return m.theme.ErrorStyle.Render("Dream mode failed: " + err.Error())
		}
		return m.theme.Success.Render("Dream mode complete.") + "\n" + result.Summary
	case "interview":
		prompt, err := m.options.Claw.BeginInterview()
		if err != nil {
			return m.theme.ErrorStyle.Render("Interview failed: " + err.Error())
		}
		return m.theme.Success.Render("Claw interview started.") + "\n" + prompt
	case "memory":
		return m.describeClawMemory()
	case "soul":
		return m.describeClawSoul()
	case "reset":
		sessionID, err := m.options.Claw.ResetChatSession()
		if err != nil {
			return m.theme.ErrorStyle.Render("Chat reset failed: " + err.Error())
		}
		return m.theme.Success.Render("Claw chat reset. New session: " + sessionID)
	case "inbox":
		if len(fields) < 3 {
			return "Usage: /claw inbox <message>"
		}
		msg, err := m.options.Claw.AddInboxMessage("mock", "user", strings.Join(fields[2:], " "))
		if err != nil {
			return m.theme.ErrorStyle.Render("Inbox failed: " + err.Error())
		}
		return m.theme.Success.Render("Claw inbox updated: ") + previewClawText(msg.Text, 80)
	case "cron":
		return m.handleClawCronCommand(fields[2:])
	default:
		return "Usage: /claw [status|start|stop|dream|interview|memory|soul|reset|inbox|cron]"
	}
}

func (m model) handleClawCronCommand(fields []string) string {
	if m.options.Claw == nil {
		return "Claw service not available."
	}
	if len(fields) == 0 || fields[0] == "list" {
		status := m.options.Claw.Status()
		if len(status.State.Crons) == 0 {
			return m.theme.Muted.Render("No Claw cron jobs. Use /claw cron add <name> <duration> <prompt>.")
		}
		rows := make([][]string, 0, len(status.State.Crons))
		for _, job := range status.State.Crons {
			next := "-"
			if !job.NextRunAt.IsZero() {
				next = job.NextRunAt.Local().Format("2006-01-02 15:04")
			}
			rows = append(rows, []string{job.Name, job.Schedule, next, job.LastResult})
		}
		return m.theme.FormatTable([]string{"Cron", "Schedule", "Next", "Last"}, rows)
	}
	if fields[0] != "add" || len(fields) < 4 {
		return "Usage: /claw cron add <name> <duration> <prompt>"
	}
	job, err := m.options.Claw.AddCron(fields[1], "@every "+fields[2], strings.Join(fields[3:], " "))
	if err != nil {
		return m.theme.ErrorStyle.Render("Cron add failed: " + err.Error())
	}
	return m.theme.Success.Render("Cron created: " + job.Name + " " + job.Schedule)
}

func (m model) describeClaw() string {
	status := m.options.Claw.Status()
	t := m.theme
	running := "off"
	if status.State.Heartbeat.Running {
		running = status.State.Heartbeat.Status
	}
	rows := [][]string{
		{"store", status.StorePath},
		{"heartbeat", running},
		{"identity", status.State.Identity.Name + " / " + status.State.Identity.Tone},
		{"soul_revision", fmt.Sprintf("%d", status.State.Soul.Revision)},
		{"memory_events", fmt.Sprintf("%d", len(status.State.Memory.Events))},
		{"memory_summaries", fmt.Sprintf("%d", len(status.State.Memory.Summaries))},
		{"suggestions", fmt.Sprintf("%d", len(status.State.Memory.Suggestions))},
		{"interview", interviewStatusText(status.State.Interview)},
		{"chat_session", status.State.Chat.SessionID},
		{"chat_turns", fmt.Sprintf("%d", len(status.State.Chat.Transcript))},
		{"agents", fmt.Sprintf("%d", len(status.State.Agents.Roles))},
		{"tools", fmt.Sprintf("%d", len(status.State.Tools.Registered))},
		{"channels", fmt.Sprintf("%d", len(status.State.Channels.Items))},
		{"crons", fmt.Sprintf("%d", len(status.State.Crons))},
		{"forge_provider", status.ActiveModel.ProviderName},
		{"forge_model", status.ActiveModel.ModelID},
		{"forge_context", fmt.Sprintf("%d/%d", status.ActiveModel.LoadedContextLength, status.ActiveModel.MaxContextLength)},
	}
	return t.FormatTable([]string{"Claw", "Value"}, rows) +
		"\n\n" + t.Muted.Render("Domains: AGENTS, SOUL, TOOLS, IDENTITY, USER, HEARTBEAT, MEMORY.") +
		"\n" + t.Muted.Render("Commands: /claw interview | /claw start | /claw stop | /claw dream | /claw reset | /claw inbox <message> | /claw cron add <name> <duration> <prompt>")
}

func (m model) describeClawMemory() string {
	status := m.options.Claw.Status()
	t := m.theme
	if len(status.State.Memory.Events) == 0 && len(status.State.Memory.Summaries) == 0 {
		return t.Muted.Render("Claw memory is empty.")
	}
	var b strings.Builder
	b.WriteString(t.Accent.Render("Memory") + "\n")
	events := status.State.Memory.Events
	if len(events) > 5 {
		events = events[len(events)-5:]
	}
	for _, event := range events {
		fmt.Fprintf(&b, "- [%s] %s: %s\n", event.Channel, event.Author, previewClawText(event.Text, 100))
	}
	if len(status.State.Memory.Summaries) > 0 {
		b.WriteString("\n" + t.Accent.Render("Summaries") + "\n")
		summaries := status.State.Memory.Summaries
		if len(summaries) > 3 {
			summaries = summaries[len(summaries)-3:]
		}
		for _, summary := range summaries {
			fmt.Fprintf(&b, "- %s\n", summary.Summary)
		}
	}
	if len(status.State.Memory.Suggestions) > 0 {
		b.WriteString("\n" + t.Accent.Render("Suggestions") + "\n")
		suggestions := status.State.Memory.Suggestions
		if len(suggestions) > 3 {
			suggestions = suggestions[len(suggestions)-3:]
		}
		for _, suggestion := range suggestions {
			fmt.Fprintf(&b, "- %s\n", suggestion.Summary)
		}
	}
	return strings.TrimSpace(b.String())
}

func (m model) describeClawSoul() string {
	status := m.options.Claw.Status()
	t := m.theme
	rows := [][]string{
		{"identity", status.State.Identity.Name},
		{"tone", status.State.Identity.Tone},
		{"style", status.State.Identity.Style},
		{"seed", status.State.Identity.Seed},
		{"values", strings.Join(status.State.Soul.Values, ", ")},
		{"goals", strings.Join(status.State.Soul.Goals, ", ")},
		{"traits", strings.Join(status.State.Soul.Traits, ", ")},
		{"user", status.State.User.DisplayName},
		{"timezone", status.State.User.Timezone},
	}
	if len(status.State.Soul.LearnedNotes) > 0 {
		rows = append(rows, []string{"learned", status.State.Soul.LearnedNotes[len(status.State.Soul.LearnedNotes)-1]})
	}
	if !status.State.Memory.LastDreamAt.IsZero() {
		rows = append(rows, []string{"last_dream", status.State.Memory.LastDreamAt.In(time.Local).Format(time.RFC3339)})
	}
	return t.FormatTable([]string{"Soul", "Value"}, rows)
}

func previewClawText(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) <= limit {
		return text
	}
	if limit <= 3 {
		return text[:limit]
	}
	return text[:limit-3] + "..."
}

func interviewStatusText(interview claw.Interview) string {
	if interview.Active {
		return "active"
	}
	if !interview.CompletedAt.IsZero() {
		return "completed"
	}
	return "not started"
}
