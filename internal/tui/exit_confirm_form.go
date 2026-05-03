package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// exitWorkspaceConfirmForm asks the user what to do when they hit
// Esc / "go to Hub" while a workspace agent task is still running.
//
// Three outcomes:
//
//	exitChoiceBackground — leave the workspace alive in memory, just
//	                       switch the UI mode to Hub. The agent keeps
//	                       running. Re-opening the same workspace path
//	                       reattaches instead of spawning fresh state.
//	exitChoiceKill       — close the workspace fully (calls
//	                       agentRuntime.Close()), then switch to Hub.
//	                       The current task is canceled mid-flight.
//	exitChoiceCancel     — dismiss the modal, stay in the workspace.
type exitWorkspaceConfirmForm struct {
	cursor   int // 0 = background, 1 = kill, 2 = cancel
	done     bool
	choice   exitChoice
	theme    Theme
	workpath string
}

type exitChoice int

const (
	exitChoiceCancel exitChoice = iota
	exitChoiceBackground
	exitChoiceKill
)

func newExitWorkspaceConfirmForm(theme Theme, workpath string) exitWorkspaceConfirmForm {
	return exitWorkspaceConfirmForm{
		// Default to Background — the safer pick when an agent is
		// running. Kill and Cancel both require a deliberate move.
		cursor:   0,
		theme:    theme,
		workpath: workpath,
	}
}

func (f exitWorkspaceConfirmForm) Update(msg tea.Msg) (exitWorkspaceConfirmForm, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return f, nil
	}
	switch key.Type {
	case tea.KeyLeft:
		if f.cursor > 0 {
			f.cursor--
		}
	case tea.KeyRight, tea.KeyTab:
		if f.cursor < 2 {
			f.cursor++
		}
	case tea.KeyEsc:
		f.choice = exitChoiceCancel
		f.done = true
	case tea.KeyEnter:
		switch f.cursor {
		case 0:
			f.choice = exitChoiceBackground
		case 1:
			f.choice = exitChoiceKill
		default:
			f.choice = exitChoiceCancel
		}
		f.done = true
	default:
		switch strings.ToLower(key.String()) {
		case "b":
			f.choice = exitChoiceBackground
			f.done = true
		case "k":
			f.choice = exitChoiceKill
			f.done = true
		case "c", "q":
			f.choice = exitChoiceCancel
			f.done = true
		}
	}
	return f, nil
}

func (f exitWorkspaceConfirmForm) View() string {
	t := f.theme
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#ffb86c")).
		Padding(1, 2).
		Width(72)

	var b strings.Builder
	b.WriteString(t.ApprovalStyle.Render("Agent task is running") + "\n\n")
	b.WriteString("Workspace: " + f.workpath + "\n")
	b.WriteString(t.Muted.Render("Choose how to leave this workspace.") + "\n\n")

	bg := "[ Background ]"
	kill := "[   Kill   ]"
	cancel := "[ Cancel ]"
	switch f.cursor {
	case 0:
		bg = t.Success.Render("> " + bg + " <")
		kill = t.Muted.Render("  " + kill + "  ")
		cancel = t.Muted.Render("  " + cancel + "  ")
	case 1:
		bg = t.Muted.Render("  " + bg + "  ")
		kill = t.ErrorStyle.Render("> " + kill + " <")
		cancel = t.Muted.Render("  " + cancel + "  ")
	default:
		bg = t.Muted.Render("  " + bg + "  ")
		kill = t.Muted.Render("  " + kill + "  ")
		cancel = t.StatusValue.Render("> " + cancel + " <")
	}
	b.WriteString(bg + "  " + kill + "  " + cancel + "\n\n")
	b.WriteString(t.Muted.Render("  [Background] keeps the agent running. Re-open the same workspace to reattach.") + "\n")
	b.WriteString(t.Muted.Render("  [Kill]       cancels the task and frees the workspace.") + "\n")
	b.WriteString(t.Muted.Render("  [Cancel]     stays here, no change.") + "\n\n")
	b.WriteString(t.Muted.Render("  Left/Right choose  Enter pick  b Background  k Kill  c/Esc Cancel"))

	return box.Render(b.String())
}
