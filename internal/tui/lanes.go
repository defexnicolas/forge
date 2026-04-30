package tui

import (
	"fmt"
	"strings"

	"forge/internal/agent"

	"github.com/charmbracelet/lipgloss"
)

// lane captures the live state of one task inside a spawn_subagents batch.
// Rendered as a single row in the inline lane block; updated in place as
// EventSubagentProgress events arrive.
type lane struct {
	Index     int
	Agent     string
	Status    string
	Phase     string
	StepsUsed int
	TimedOut  bool
	Summary   string
	Error     string
}

// laneGroup owns the slice of lanes for one in-flight batch and remembers
// where the block was spliced into m.history so updates can rewrite the
// same lines instead of appending new ones.
type laneGroup struct {
	BatchID   string
	Lanes     []lane
	StartIdx  int
	LineCount int
}

// applyProgress merges a SubagentProgress event into the group, extending
// the lanes slice when needed. Returns true if the event changed state.
func (g *laneGroup) applyProgress(p *agent.SubagentProgress) bool {
	if g == nil || p == nil {
		return false
	}
	if p.Index < 0 {
		return false
	}
	for len(g.Lanes) <= p.Index {
		g.Lanes = append(g.Lanes, lane{Index: len(g.Lanes), Status: "pending"})
	}
	ln := &g.Lanes[p.Index]
	if p.Agent != "" {
		ln.Agent = p.Agent
	}
	if p.Status != "" {
		ln.Status = p.Status
	}
	if p.Phase != "" {
		ln.Phase = p.Phase
	}
	if p.StepsUsed > 0 {
		ln.StepsUsed = p.StepsUsed
	}
	if p.TimedOut {
		ln.TimedOut = true
	}
	if p.Summary != "" {
		ln.Summary = p.Summary
	}
	if p.Error != "" {
		ln.Error = p.Error
	}
	return true
}

// renderLanes returns the visual lines for the lane block. Each lane is a
// single row: "    ◌ [1] explorer   running   searching for fs.go". Width
// is the full viewport width; callers indent by the same 4-space prefix
// used elsewhere.
func renderLanes(group *laneGroup, theme Theme, width int) []string {
	if group == nil || len(group.Lanes) == 0 {
		return nil
	}
	if width < 40 {
		width = 40
	}
	maxAgent := 10
	for _, ln := range group.Lanes {
		if n := len(ln.Agent); n > maxAgent {
			maxAgent = n
		}
	}
	if maxAgent > 16 {
		maxAgent = 16
	}
	out := make([]string, 0, len(group.Lanes)+1)
	header := fmt.Sprintf("    parallel subagents (%d)", len(group.Lanes))
	out = append(out, theme.Muted.Italic(true).Render(header))
	for _, ln := range group.Lanes {
		out = append(out, renderLaneRow(ln, theme, maxAgent, width))
	}
	return out
}

func renderLaneRow(ln lane, theme Theme, agentWidth, vpWidth int) string {
	marker, statusStyle := laneMarker(ln.Status, theme)
	agentCell := padRight(truncatePlain(ln.Agent, agentWidth), agentWidth)
	statusCell := padRight(ln.Status, 9)
	detail := ln.Summary
	if ln.Error != "" {
		detail = ln.Error
	}
	if ln.TimedOut {
		detail = "timeout"
		if ln.Error != "" {
			detail += ": " + ln.Error
		}
	} else if ln.Phase != "" && ln.Status == "running" {
		detail = ln.Phase
		if ln.StepsUsed > 0 {
			detail += fmt.Sprintf(" step:%d", ln.StepsUsed)
		}
	} else if ln.Phase != "" && ln.Status == "error" {
		detail = ln.Phase
		if ln.Error != "" {
			detail += ": " + ln.Error
		}
	}
	// Leave room for: 4 indent + marker (2) + "[N] " + agentCell + " " +
	// statusCell + " " + detail. detail gets whatever's left.
	fixed := 4 + 2 + 4 + agentWidth + 1 + 9 + 1
	detailWidth := vpWidth - fixed
	if detailWidth < 20 {
		detailWidth = 20
	}
	detail = truncatePlain(strings.ReplaceAll(detail, "\n", " "), detailWidth)
	return fmt.Sprintf("    %s[%d] %s %s %s",
		marker,
		ln.Index,
		theme.ToolCallStyle.Render(agentCell),
		statusStyle.Render(statusCell),
		theme.Muted.Render(detail),
	)
}

// laneMarker returns the leading glyph + matching style for a given lane
// status. Uses plain ASCII glyphs so Windows terminals without full unicode
// fonts don't render tofu.
func laneMarker(status string, theme Theme) (string, lipgloss.Style) {
	switch status {
	case "pending":
		return theme.Muted.Render("o "), theme.Muted
	case "running":
		return theme.Warning.Render("> "), theme.Warning
	case "completed":
		return theme.Success.Render("+ "), theme.Success
	case "error":
		return theme.ErrorStyle.Render("x "), theme.ErrorStyle
	default:
		return theme.Muted.Render("- "), theme.Muted
	}
}

func padRight(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(s))
}

func truncatePlain(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(s) <= limit {
		return s
	}
	if limit <= 3 {
		return s[:limit]
	}
	return s[:limit-3] + "..."
}
