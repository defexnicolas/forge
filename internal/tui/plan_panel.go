package tui

import (
	"fmt"
	"strings"

	"forge/internal/tasks"

	"github.com/charmbracelet/lipgloss"
)

const planPanelWidth = 32

// planPhase enumerates the workflow states we surface in the plan panel
// header. Derivation lives in DerivePlanPhase so the caller doesn't have to
// couple panel rendering to runtime internals.
type planPhase int

const (
	phaseExplore planPhase = iota
	phaseDesign
	phaseReview
	phaseExecute
)

// DerivePlanPhase maps (hasPlan, tasks) into the current plan-mode phase.
// EXPLORE when no plan and no tasks; DESIGN when a plan exists but the
// checklist is empty; REVIEW when tasks are listed but nothing is in_progress
// or completed yet; EXECUTE once the user has accepted work.
func DerivePlanPhase(hasPlan bool, taskList []tasks.Task) planPhase {
	if !hasPlan && len(taskList) == 0 {
		return phaseExplore
	}
	if hasPlan && len(taskList) == 0 {
		return phaseDesign
	}
	for _, t := range taskList {
		if t.Status == "in_progress" || t.Status == "running" ||
			t.Status == "completed" || t.Status == "done" {
			return phaseExecute
		}
	}
	return phaseReview
}

// RenderPlanPanel builds a vertical checklist panel for the right side.
// The panel grows to fit its content, not the full viewport height.
func RenderPlanPanel(taskList []tasks.Task, hasPlan bool, height int, theme Theme) string {
	if len(taskList) == 0 && !hasPlan {
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
	maxLines := height - 5
	if maxLines > 0 && len(lines) > maxLines {
		lines = lines[:maxLines]
	}

	content := strings.Join(lines, "\n")
	if content == "" {
		content = theme.Muted.Italic(true).Render(" (no tasks yet)")
	}

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("245")).
		Width(planPanelWidth)

	phase := DerivePlanPhase(hasPlan, taskList)
	header := theme.Accent.Render(" Checklist")
	phaseLine := renderPhaseIndicator(phase, theme)
	return header + "\n" + phaseLine + "\n" + box.Render(content)
}

// renderPhaseIndicator draws EXPLORE → DESIGN → REVIEW → EXECUTE with the
// active phase highlighted. Kept single-line by abbreviating when the panel
// width would otherwise force wrap.
func renderPhaseIndicator(active planPhase, theme Theme) string {
	phases := []struct {
		tag  string
		full string
	}{
		{"EXP", "EXPLORE"},
		{"DSN", "DESIGN"},
		{"REV", "REVIEW"},
		{"EXE", "EXECUTE"},
	}
	parts := make([]string, 0, len(phases))
	for i, p := range phases {
		label := p.tag
		if planPhase(i) == active {
			label = theme.Accent.Bold(true).Render(p.full)
		} else {
			label = theme.Muted.Render(p.tag)
		}
		parts = append(parts, label)
	}
	return " " + strings.Join(parts, theme.Muted.Render(" → "))
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
