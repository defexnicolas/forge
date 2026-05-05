package tui

import (
	"fmt"
	"strings"
	"sync"

	"github.com/charmbracelet/glamour"
)

// markdownRenderer wraps a Glamour TermRenderer with a width cache. Rebuilt
// on viewport resize. nil-safe: if glamour fails to initialize (unusual) the
// Render method returns the input unchanged so the TUI never dies on a
// missing renderer.
type markdownRenderer struct {
	mu       sync.Mutex
	renderer *glamour.TermRenderer
	width    int
	style    string
}

func newMarkdownRenderer(width int, themeName string) *markdownRenderer {
	if width < 40 {
		width = 40
	}
	style := markdownStyleFor(themeName)
	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle(style),
		glamour.WithWordWrap(width-8),
	)
	if err != nil {
		return &markdownRenderer{width: width, style: style}
	}
	return &markdownRenderer{renderer: r, width: width, style: style}
}

// Resize rebuilds the underlying Glamour renderer when the viewport width
// changes enough to matter. Small deltas are absorbed to avoid re-init churn
// on every WindowSizeMsg (resizing emits dozens of events).
func (r *markdownRenderer) Resize(width int, themeName string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if width < 40 {
		width = 40
	}
	newStyle := markdownStyleFor(themeName)
	if abs(width-r.width) < 4 && newStyle == r.style && r.renderer != nil {
		return
	}
	tr, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle(newStyle),
		glamour.WithWordWrap(width-8),
	)
	if err != nil {
		return
	}
	r.renderer = tr
	r.width = width
	r.style = newStyle
}

// Render returns the markdown-rendered version of text, or text unchanged
// if rendering fails. The returned string is right-trimmed so callers can
// indent each line without trailing padding.
func (r *markdownRenderer) Render(text string) string {
	if r == nil || r.renderer == nil {
		return text
	}
	r.mu.Lock()
	out, err := r.renderer.Render(text)
	r.mu.Unlock()
	if err != nil {
		return text
	}
	return strings.TrimRight(out, "\n")
}

// hasMarkdown returns true when text contains formatting cues worth routing
// through Glamour. Heuristic — avoids the Glamour pipeline for short plain
// answers where it would only add wrapping noise.
func hasMarkdown(text string) bool {
	if strings.Contains(text, "```") {
		return true
	}
	if strings.Contains(text, "\n#") || strings.HasPrefix(text, "# ") || strings.HasPrefix(text, "## ") {
		return true
	}
	if strings.Contains(text, "\n- ") || strings.HasPrefix(text, "- ") {
		return true
	}
	if strings.Contains(text, "\n* ") || strings.HasPrefix(text, "* ") {
		return true
	}
	if strings.Contains(text, "\n> ") || strings.HasPrefix(text, "> ") {
		return true
	}
	if strings.Contains(text, "**") {
		return true
	}
	if strings.Count(text, "`") >= 2 {
		return true
	}
	return false
}

// thinkSplit carries the three parts of an assistant response that may
// contain a <think>...</think> block. Any field can be empty.
type thinkSplit struct {
	before, thinking, after string
	hasThink                bool
}

// splitThinking extracts a <think>...</think> block from text, returning
// the pre/think/post segments. When the closing tag is absent, everything
// after the opening tag is treated as thinking.
func splitThinking(text string) thinkSplit {
	const open = "<think>"
	const close = "</think>"
	start := strings.Index(text, open)
	if start < 0 {
		return thinkSplit{before: text}
	}
	res := thinkSplit{hasThink: true, before: strings.TrimSpace(text[:start])}
	rest := text[start+len(open):]
	end := strings.Index(rest, close)
	if end < 0 {
		res.thinking = rest
		return res
	}
	res.thinking = rest[:end]
	res.after = strings.TrimSpace(rest[end+len(close):])
	return res
}

func markdownStyleFor(themeName string) string {
	switch themeName {
	case "light":
		return "light"
	case "mono":
		return "notty"
	default:
		return "dark"
	}
}

// thinkingPeekChars caps the rolling preview when thinking is collapsed.
// Tuned so the peek fits on one line at most realistic terminal widths
// (~120 cols minus the 4-space indent) while still surfacing enough of
// the reasoning to be informative.
const thinkingPeekChars = 100

// formatStreamingText renders the raw streamed text for the viewport, applying
// the <think> filter controlled by thinkEnabled. Unlike formatAssistantBlock
// (which runs once at turn end through Glamour), this runs on every flush
// tick — so it stays cheap and avoids ANSI-preserving markdown reflow.
//
// When thinkEnabled is true: thinking spans are shown inline in full with
// muted italic styling so the reasoning is legible but visually separated
// from the final answer.
//
// When thinkEnabled is false: a rolling "peek" of the last ~100 chars of
// the in-progress reasoning is shown, plus a compact "[thinking, Nc]"
// marker for completed blocks. The peek is what tells the user what the
// model is reasoning about right now without flooding the viewport.
func formatStreamingText(raw string, thinkEnabled bool, theme Theme) string {
	const open = "<think>"
	const close = "</think>"
	if !strings.Contains(raw, open) {
		return raw
	}
	var b strings.Builder
	b.Grow(len(raw))
	remaining := raw
	for {
		start := strings.Index(remaining, open)
		if start < 0 {
			b.WriteString(remaining)
			break
		}
		b.WriteString(remaining[:start])
		rest := remaining[start+len(open):]
		end := strings.Index(rest, close)
		if end < 0 {
			// Still mid-stream inside <think> — no closing tag yet.
			thinking := strings.TrimSpace(rest)
			if thinkEnabled {
				b.WriteString(theme.Muted.Italic(true).Render("[ thinking: " + thinking + " ]"))
			} else {
				b.WriteString(theme.Muted.Italic(true).Render("[ thinking ⋯ " + tailRunes(thinking, thinkingPeekChars) + " ]"))
			}
			return b.String()
		}
		thinking := strings.TrimSpace(rest[:end])
		if thinkEnabled {
			b.WriteString(theme.Muted.Italic(true).Render("[ thinking: " + thinking + " ]"))
		} else {
			b.WriteString(theme.Muted.Render(fmt.Sprintf("[thinking, %dc — Ctrl+T to expand]", len([]rune(thinking)))))
		}
		remaining = rest[end+len(close):]
	}
	return b.String()
}

// tailRunes returns the last n runes of s (or all of s if shorter), with a
// leading "…" when truncation happened. Operating on runes preserves
// multi-byte UTF-8 chars when the model emits non-ASCII reasoning.
func tailRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return "…" + string(r[len(r)-n:])
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
