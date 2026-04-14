package tui

import (
	"fmt"
	"strings"

	"forge/internal/tasks"

	"github.com/charmbracelet/lipgloss"
)

const planPanelWidth = 32

// RenderPlanPanel builds a vertical checklist panel for the right side.
// The panel grows to fit its content, not the full viewport height.
func RenderPlanPanel(taskList []tasks.Task, height int, theme Theme) string {
	if len(taskList) == 0 {
		return ""
	}

	var lines []string
	for _, task := range taskList {
		icon := "[ ]"
		style := theme.TableRow
		switch task.Status {
		case "completed", "done":
			icon = "[x]"
			style = theme.Success
		case "in_progress", "running":
			icon = "[>]"
			style = theme.Warning
		}
		title := task.Title
		if len(title) > planPanelWidth-5 {
			title = title[:planPanelWidth-8] + "..."
		}
		lines = append(lines, style.Render(fmt.Sprintf(" %s %s", icon, title)))
	}

	// Cap at viewport height but don't pad - only as tall as needed.
	maxLines := height - 3
	if maxLines > 0 && len(lines) > maxLines {
		lines = lines[:maxLines]
	}

	content := strings.Join(lines, "\n")

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("245")).
		Width(planPanelWidth)

	header := theme.Accent.Render(" Plan")
	return header + "\n" + box.Render(content)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
