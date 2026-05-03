package tui

import (
	"strings"

	"forge/internal/agent"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type approvalForm struct {
	request    *agent.ApprovalRequest
	cursor     int // 0 = auto, 1 = approve, 2 = deny
	done       bool
	approved   bool
	autoMode   bool // true when the user picked [Auto] -- caller flips approval_profile globally
	theme      Theme
	width      int
	height     int
	diffLines  []string
	diffOffset int
	adds       int
	dels       int
}

const approvalDiffPageDefault = 18

func newApprovalForm(req *agent.ApprovalRequest, theme Theme, width, height int) approvalForm {
	f := approvalForm{
		request: req,
		// Default cursor = Approve (the safe, single-action choice).
		// The caller can later persist Auto if the user explicitly picks
		// it, but we never default to it.
		cursor: 1,
		theme:  theme,
		width:  width,
		height: height,
	}
	if req != nil && req.Diff != "" {
		colored := theme.FormatDiffColored(req.Diff)
		f.diffLines = strings.Split(strings.TrimRight(colored, "\n"), "\n")
		for _, line := range strings.Split(req.Diff, "\n") {
			switch {
			case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---"):
				// file headers, ignore in counts
			case strings.HasPrefix(line, "+"):
				f.adds++
			case strings.HasPrefix(line, "-"):
				f.dels++
			}
		}
	}
	return f
}

func (f approvalForm) pageSize() int {
	page := f.height - 12
	if page < 6 {
		page = approvalDiffPageDefault
	}
	if page > 30 {
		page = 30
	}
	return page
}

func (f approvalForm) Update(msg tea.Msg) (approvalForm, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return f, nil
	}
	page := f.pageSize()
	total := len(f.diffLines)
	maxOff := total - page
	if maxOff < 0 {
		maxOff = 0
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
	case tea.KeyUp:
		if f.diffOffset > 0 {
			f.diffOffset--
		}
	case tea.KeyDown:
		if f.diffOffset < maxOff {
			f.diffOffset++
		}
	case tea.KeyPgUp:
		f.diffOffset -= page
		if f.diffOffset < 0 {
			f.diffOffset = 0
		}
	case tea.KeyPgDown:
		f.diffOffset += page
		if f.diffOffset > maxOff {
			f.diffOffset = maxOff
		}
	case tea.KeyHome:
		f.diffOffset = 0
	case tea.KeyEnd:
		f.diffOffset = maxOff
	case tea.KeyEsc:
		f.approved = false
		f.done = true
	case tea.KeyEnter:
		switch f.cursor {
		case 0:
			f.autoMode = true
			f.approved = true
		case 1:
			f.approved = true
		default:
			f.approved = false
		}
		f.done = true
	default:
		switch strings.ToLower(key.String()) {
		case "u":
			f.autoMode = true
			f.approved = true
			f.done = true
		case "y", "a":
			f.approved = true
			f.done = true
		case "n", "d":
			f.approved = false
			f.done = true
		}
	}
	return f, nil
}

func (f approvalForm) View() string {
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
		BorderForeground(lipgloss.Color("#ffb86c")).
		Padding(1, 2).
		Width(width)

	var b strings.Builder
	b.WriteString(t.ApprovalStyle.Render("Approval required") + "\n")
	b.WriteString(t.StatusKey.Render("Tool: ") + t.ToolCallStyle.Render(f.request.ToolName) + "\n")
	if f.request.Summary != "" {
		b.WriteString(t.StatusKey.Render("Summary: ") + f.request.Summary + "\n")
	}
	b.WriteString("\n")
	if len(f.diffLines) > 0 {
		total := len(f.diffLines)
		page := f.pageSize()
		if page > total {
			page = total
		}
		off := f.diffOffset
		if off > total-page {
			off = total - page
		}
		if off < 0 {
			off = 0
		}
		end := off + page
		if end > total {
			end = total
		}
		addStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#50fa7b"))
		delStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#ff5555"))
		header := t.StatusKey.Render("Diff: ") +
			addStyle.Render("+"+itoa(f.adds)) + " " +
			delStyle.Render("-"+itoa(f.dels))
		b.WriteString(header + "\n")
		b.WriteString(strings.Join(f.diffLines[off:end], "\n") + "\n")
		footer := t.Muted.Render("  lines " + itoa(off+1) + "-" + itoa(end) + " / " + itoa(total))
		b.WriteString(footer + "\n\n")
	} else if len(f.request.Input) > 0 {
		b.WriteString(t.Muted.Render("Input: ") + truncate(string(f.request.Input), 400) + "\n\n")
	}

	auto := "[  Auto  ]"
	approve := "[ Approve ]"
	deny := "[  Deny  ]"
	switch f.cursor {
	case 0:
		auto = t.ApprovalStyle.Render("> " + auto + " <")
		approve = t.Muted.Render("  " + approve + "  ")
		deny = t.Muted.Render("  " + deny + "  ")
	case 1:
		auto = t.Muted.Render("  " + auto + "  ")
		approve = t.Success.Render("> " + approve + " <")
		deny = t.Muted.Render("  " + deny + "  ")
	default:
		auto = t.Muted.Render("  " + auto + "  ")
		approve = t.Muted.Render("  " + approve + "  ")
		deny = t.ErrorStyle.Render("> " + deny + " <")
	}
	b.WriteString(auto + "  " + approve + "  " + deny + "\n")
	b.WriteString("\n" + t.Muted.Render("  [Auto] approves now and stops asking (sets approval_profile = auto globally)"))
	b.WriteString("\n" + t.Muted.Render("  Left/Right choose  Up/Dn PgUp/PgDn Home/End scroll  Enter pick  u Auto  y/a Approve  n/d Deny  Esc reject"))

	return box.Render(b.String())
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	sign := ""
	if n < 0 {
		sign = "-"
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return sign + string(buf[i:])
}
