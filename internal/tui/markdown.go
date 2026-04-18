package tui

import (
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

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
