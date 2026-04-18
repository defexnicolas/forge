package tui

import (
	"fmt"
	"strings"

	"forge/internal/tools"

	"github.com/charmbracelet/lipgloss"
)

const maxDiffLines = 20

// diffResultTools enumerates the tools whose Result.Content[0].Text is a
// unified-diff string produced by internal/patch.Diff. These trigger
// FormatPatchSummary in EventToolResult instead of the plain text summary.
var diffResultTools = map[string]bool{
	"edit_file":   true,
	"write_file":  true,
	"apply_patch": true,
}

// isDiffResultTool reports whether a tool's successful Result payload is a
// unified diff suitable for the diff renderer.
func isDiffResultTool(name string) bool { return diffResultTools[name] }

// extractResultDiff returns the unified diff text stored in a mutating tool's
// Result, or empty string if none is present. edit.go/write.go/apply_patch
// put the diff in Content[0] with Type="text".
func extractResultDiff(result *tools.Result) string {
	if result == nil {
		return ""
	}
	for _, block := range result.Content {
		if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			return block.Text
		}
	}
	return ""
}

// diffFilePath picks the file label for FormatPatchSummary. Mutating tools
// set Result.Summary to the relative path for single-file ops; apply_patch
// sets it to "Applied unified diff", so fall back to ChangedFiles.
func diffFilePath(result *tools.Result) string {
	if result == nil {
		return ""
	}
	if len(result.ChangedFiles) == 1 {
		return result.ChangedFiles[0]
	}
	if len(result.ChangedFiles) > 1 {
		return fmt.Sprintf("%d files", len(result.ChangedFiles))
	}
	return strings.TrimSpace(result.Summary)
}

// diff line styles with background colors
var (
	diffAddedStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("22")).
			Foreground(lipgloss.Color("255"))
	diffRemovedStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("52")).
				Foreground(lipgloss.Color("255"))
	diffHeaderStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("236")).
			Foreground(lipgloss.Color("75")).
			Bold(true)
	diffHunkStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("236")).
			Foreground(lipgloss.Color("141"))
	diffContextStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("245"))
)

// FormatDiffColored renders a unified diff with background colors.
func (t Theme) FormatDiffColored(diff string) string {
	if diff == "" {
		return t.Muted.Render("No changes.")
	}
	var b strings.Builder
	lines := strings.Split(diff, "\n")
	for _, line := range lines {
		b.WriteString(renderDiffLine(line) + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// FormatPatchSummary renders a patch summary with a capped diff preview.
// Uses "  " prefix to align with tool call indicators.
func (t Theme) FormatPatchSummary(file string, added, removed int, diffText string) string {
	var b strings.Builder
	b.WriteString("    " + t.IndicatorTool.Render("* ") + t.StatusValue.Render("Update("+file+")") + "\n")
	b.WriteString("      " + t.Muted.Render(fmt.Sprintf("-> Added %d lines, removed %d lines", added, removed)) + "\n")
	if diffText != "" {
		lines := strings.Split(diffText, "\n")
		shown := 0
		for _, line := range lines {
			if shown >= maxDiffLines {
				b.WriteString("        " + t.Muted.Render(fmt.Sprintf("... +%d more lines (use /diff to see full)", len(lines)-shown)) + "\n")
				break
			}
			b.WriteString("        " + renderDiffLine(line) + "\n")
			shown++
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderDiffLine applies background-colored styling to a single diff line.
func renderDiffLine(line string) string {
	switch {
	case strings.HasPrefix(line, "--- "), strings.HasPrefix(line, "+++ "):
		return diffHeaderStyle.Render(line)
	case strings.HasPrefix(line, "@@"):
		return diffHunkStyle.Render(line)
	case strings.HasPrefix(line, "+"):
		return diffAddedStyle.Render(line)
	case strings.HasPrefix(line, "-"):
		return diffRemovedStyle.Render(line)
	case strings.HasPrefix(line, "diff --git"):
		return diffHeaderStyle.Render(line)
	default:
		return diffContextStyle.Render(line)
	}
}

// CountDiffChanges counts added and removed lines in a unified diff.
func CountDiffChanges(diff string) (added, removed int) {
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			added++
		}
		if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			removed++
		}
	}
	return
}
