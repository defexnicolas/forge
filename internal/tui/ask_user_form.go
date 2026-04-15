package tui

import (
	"fmt"
	"strings"

	"forge/internal/agent"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// askUserForm is the modal that pops when the agent calls the `ask_user`
// tool. Shows the question, up to 3 model-suggested options, and a final
// "Write my own answer" row. Arrow keys navigate; Enter picks. On the
// "write my own" row the textarea captures free text.
type askUserForm struct {
	request *agent.AskUserRequest
	input   textarea.Model
	cursor  int // 0..len(Options) — last index is always the free-text row
	rows    int // total number of rows (len(Options) + 1)
	done    bool
	answer  string
	theme   Theme
	width   int
	height  int
}

const askUserInputDefaultLines = 3
const askUserInputMaxLines = 12

func newAskUserForm(req *agent.AskUserRequest, theme Theme, width, height int) askUserForm {
	ta := textarea.New()
	ta.Placeholder = "Type your own answer. Enter submits, Shift+Enter for newline."
	ta.CharLimit = 4096
	ta.ShowLineNumbers = false
	ta.Prompt = ""
	w := width - 8
	if w < 40 {
		w = 40
	}
	if w > 120 {
		w = 120
	}
	ta.SetWidth(w)
	ta.SetHeight(askUserInputDefaultLines)

	f := askUserForm{
		request: req,
		input:   ta,
		theme:   theme,
		width:   width,
		height:  height,
	}
	if req != nil {
		f.rows = len(req.Options) + 1
		// Default cursor: first suggestion when options exist, otherwise the
		// free-text row (so the textarea is immediately focused).
		f.cursor = 0
		if len(req.Options) == 0 {
			f.cursor = 0 // single row = free-text
		}
	} else {
		f.rows = 1
	}
	f.syncFocus()
	return f
}

func (f *askUserForm) onFreeTextRow() bool {
	if f.request == nil {
		return true
	}
	return f.cursor == len(f.request.Options)
}

func (f *askUserForm) syncFocus() {
	if f.onFreeTextRow() {
		f.input.Focus()
	} else {
		f.input.Blur()
	}
}

func (f askUserForm) Update(msg tea.Msg) (askUserForm, tea.Cmd) {
	key, isKey := msg.(tea.KeyMsg)

	if isKey {
		switch key.Type {
		case tea.KeyEsc, tea.KeyCtrlC:
			f.answer = ""
			f.done = true
			return f, nil
		case tea.KeyUp:
			if f.cursor > 0 {
				f.cursor--
				f.syncFocus()
			}
			return f, nil
		case tea.KeyDown:
			if f.cursor < f.rows-1 {
				f.cursor++
				f.syncFocus()
			}
			return f, nil
		case tea.KeyEnter:
			if !f.onFreeTextRow() {
				// Pick the selected suggestion.
				f.answer = strings.TrimSpace(f.request.Options[f.cursor])
				if f.answer != "" {
					f.done = true
				}
				return f, nil
			}
			// Free-text row: plain Enter submits, Alt+Enter inserts newline.
			if !key.Alt {
				text := strings.TrimSpace(f.input.Value())
				if text == "" {
					return f, nil
				}
				f.answer = text
				f.done = true
				return f, nil
			}
		}
	}

	// Only route other keystrokes to the textarea when the free-text row is
	// focused — otherwise arrows etc. wouldn't navigate the option list.
	if f.onFreeTextRow() {
		var cmd tea.Cmd
		f.input, cmd = f.input.Update(msg)
		lines := strings.Count(f.input.Value(), "\n") + 1
		if lines < askUserInputDefaultLines {
			lines = askUserInputDefaultLines
		}
		if lines > askUserInputMaxLines {
			lines = askUserInputMaxLines
		}
		if lines != f.input.Height() {
			f.input.SetHeight(lines)
		}
		return f, cmd
	}
	return f, nil
}

func (f askUserForm) View() string {
	t := f.theme
	if f.request == nil {
		return ""
	}
	width := f.width - 4
	if width < 40 {
		width = 40
	}
	if width > 120 {
		width = 120
	}
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#7fb8ff")).
		Padding(1, 2).
		Width(width)

	var b strings.Builder
	header := "Agent is asking"
	if f.request.Total > 1 {
		header = fmt.Sprintf("Agent is asking (%d of %d)", f.request.Index+1, f.request.Total)
	}
	b.WriteString(t.ApprovalStyle.Render(header) + "\n\n")
	for _, line := range strings.Split(strings.TrimRight(f.request.Question, "\n"), "\n") {
		b.WriteString(t.StatusValue.Render(line) + "\n")
	}
	b.WriteString("\n")

	// Options list.
	for i, opt := range f.request.Options {
		marker := "  "
		line := opt
		if f.cursor == i {
			marker = t.Success.Render("> ")
			line = t.StatusValue.Render(opt)
		} else {
			line = t.Muted.Render(opt)
		}
		fmt.Fprintf(&b, "%s%s\n", marker, line)
	}

	// Free-text row.
	freeMarker := "  "
	freeLabel := t.Muted.Render("Write my own answer")
	if f.onFreeTextRow() {
		freeMarker = t.Success.Render("> ")
		freeLabel = t.StatusValue.Render("Write my own answer")
	}
	fmt.Fprintf(&b, "%s%s\n", freeMarker, freeLabel)
	if f.onFreeTextRow() {
		b.WriteString(f.input.View())
		b.WriteString("\n")
	}

	b.WriteString("\n")
	if len(f.request.Options) > 0 {
		b.WriteString(t.Muted.Render("  ↑/↓ select · Enter pick/submit · Esc skip · Shift+Enter newline"))
	} else {
		b.WriteString(t.Muted.Render("  Enter submit · Shift+Enter newline · Esc skip"))
	}
	return box.Render(b.String())
}
