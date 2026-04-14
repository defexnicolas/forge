package contextbuilder

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ResolveMention resolves a single @mention into a context Item.
// Supported types: @file, @folder:path, @diff, @last-error, @agent:name.
func (b *Builder) ResolveMention(mention string) Item {
	// @diff — workspace git diff
	if mention == "diff" || mention == "git:diff" {
		return b.resolveDiff()
	}
	// @last-error — last error from session
	if mention == "last-error" || mention == "last_error" {
		return b.resolveLastError()
	}
	// @agent:name — subagent description
	if strings.HasPrefix(mention, "agent:") {
		name := strings.TrimPrefix(mention, "agent:")
		return Item{
			Kind:    "agent",
			Path:    name,
			Content: "Subagent reference: " + name + ". Use /agents for details.",
		}
	}
	// @folder:path — directory listing
	if strings.HasPrefix(mention, "folder:") {
		path := strings.TrimPrefix(mention, "folder:")
		return b.resolveFolder(path)
	}
	// @symbol:name — LSP symbol lookup
	if strings.HasPrefix(mention, "symbol:") {
		name := strings.TrimPrefix(mention, "symbol:")
		return b.resolveSymbol(name)
	}
	// @diagnostics or @diagnostics:file — LSP diagnostics
	if mention == "diagnostics" || strings.HasPrefix(mention, "diagnostics:") {
		file := strings.TrimPrefix(mention, "diagnostics:")
		if file == "diagnostics" {
			file = ""
		}
		return b.resolveDiagnostics(file)
	}
	// Default: treat as file path
	return b.readOptional(mention, "mention")
}

func (b *Builder) resolveDiff() Item {
	cmd := exec.Command("git", "-C", b.CWD, "diff")
	out, err := cmd.Output()
	if err != nil {
		return Item{Kind: "diff", Error: "git diff: " + err.Error()}
	}
	text := strings.TrimSpace(string(out))
	if text == "" {
		return Item{Kind: "diff", Content: "No workspace diff."}
	}
	return Item{Kind: "diff", Content: text}
}

func (b *Builder) resolveLastError() Item {
	if b.History == nil {
		return Item{Kind: "last-error", Error: "no session history available"}
	}
	text := b.History.ContextText(20)
	// Find last error line in session
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if strings.Contains(line, "error") || strings.Contains(line, "Error") || strings.Contains(line, "failed") {
			return Item{Kind: "last-error", Content: line}
		}
	}
	return Item{Kind: "last-error", Content: "No recent errors found."}
}

func (b *Builder) resolveSymbol(name string) Item {
	if b.LSP == nil {
		return Item{Kind: "symbol", Path: name, Error: "LSP not configured"}
	}
	// Search for symbol in recently mentioned/pinned files.
	var found []string
	if b.Tray != nil {
		if pins, err := b.Tray.Pins(); err == nil {
			for _, pin := range pins {
				symbols, err := b.LSP.Symbols(filepath.Join(b.CWD, pin.Path))
				if err != nil {
					continue
				}
				for _, sym := range symbols {
					if strings.Contains(strings.ToLower(sym), strings.ToLower(name)) {
						found = append(found, pin.Path+": "+sym)
					}
				}
			}
		}
	}
	if len(found) == 0 {
		return Item{Kind: "symbol", Path: name, Error: "Symbol not found: " + name}
	}
	return Item{
		Kind:    "symbol",
		Path:    name,
		Content: "Symbol matches for " + name + ":\n" + strings.Join(found, "\n"),
	}
}

func (b *Builder) resolveDiagnostics(file string) Item {
	if b.LSP == nil {
		return Item{Kind: "diagnostics", Error: "LSP not configured"}
	}
	if file == "" {
		return Item{Kind: "diagnostics", Error: "Usage: @diagnostics:path/to/file"}
	}
	resolved := filepath.Join(b.CWD, file)
	diags, err := b.LSP.Diagnostics(resolved)
	if err != nil {
		return Item{Kind: "diagnostics", Path: file, Error: err.Error()}
	}
	if len(diags) == 0 {
		return Item{Kind: "diagnostics", Path: file, Content: "No diagnostics for " + file}
	}
	var lines []string
	for _, d := range diags {
		lines = append(lines, fmt.Sprintf("L%d [%s] %s", d.Line, d.Severity, d.Message))
	}
	return Item{
		Kind:    "diagnostics",
		Path:    file,
		Content: strings.Join(lines, "\n"),
	}
}

func (b *Builder) resolveFolder(path string) Item {
	resolved, err := workspacePath(b.CWD, path)
	if err != nil {
		return Item{Kind: "folder", Path: path, Error: err.Error()}
	}
	entries, err := os.ReadDir(resolved)
	if err != nil {
		return Item{Kind: "folder", Path: path, Error: err.Error()}
	}
	var lines []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			name += "/"
		}
		lines = append(lines, name)
	}
	return Item{
		Kind:    "folder",
		Path:    filepath.ToSlash(path),
		Content: strings.Join(lines, "\n"),
	}
}
