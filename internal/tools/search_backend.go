package tools

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Search backend strategy: prefer ripgrep (`rg`) when available because it's
// 5-50x faster than the Go fallback on real repos, respects `.gitignore`, and
// already skips binary files. The Go fallback is kept so the tool works on
// machines without rg installed and so tests can exercise the fallback path
// deterministically (set `forgeForceGoSearchBackend` in tests).

var (
	rgPathOnce sync.Once
	rgPath     string
	rgErr      error
)

// forgeForceGoSearchBackend, when true, makes ripgrepPath() return "" so the
// Go fallback runs even on machines that have rg installed. Used by tests.
var forgeForceGoSearchBackend bool

func ripgrepPath() string {
	rgPathOnce.Do(func() {
		rgPath, rgErr = exec.LookPath("rg")
	})
	if forgeForceGoSearchBackend {
		return ""
	}
	if rgErr != nil {
		return ""
	}
	return rgPath
}

// Directories that almost never contain source the user wants to search and
// that blow up walk time when included. Used by the Go fallback only -- rg
// honors .gitignore and skips most of these on its own.
var skipDirs = map[string]struct{}{
	".git":         {},
	".hg":          {},
	".svn":         {},
	".idea":        {},
	".vscode":      {},
	".forge":       {},
	".claude":      {},
	"node_modules": {},
	"vendor":       {},
	"dist":         {},
	"build":        {},
	"out":          {},
	"target":       {},
	".next":        {},
	".nuxt":        {},
	".cache":       {},
	"__pycache__":  {},
	".pytest_cache": {},
	"coverage":     {},
}

// File extensions the Go fallback skips because they're almost always binary
// or huge generated assets. rg detects binary content automatically; this list
// is just a fast pre-filter for the fallback.
var skipExts = map[string]struct{}{
	".exe": {}, ".bin": {}, ".so": {}, ".dll": {}, ".dylib": {}, ".a": {},
	".o": {}, ".class": {}, ".jar": {}, ".pyc": {}, ".pyo": {},
	".png": {}, ".jpg": {}, ".jpeg": {}, ".gif": {}, ".bmp": {}, ".ico": {},
	".webp": {}, ".tiff": {}, ".svg": {},
	".mp3": {}, ".mp4": {}, ".mov": {}, ".avi": {}, ".webm": {}, ".wav": {},
	".zip": {}, ".tar": {}, ".gz": {}, ".7z": {}, ".rar": {}, ".bz2": {},
	".pdf": {}, ".doc": {}, ".docx": {}, ".xls": {}, ".xlsx": {},
	".db": {}, ".sqlite": {}, ".sqlite3": {},
	".woff": {}, ".woff2": {}, ".ttf": {}, ".otf": {}, ".eot": {},
}

func shouldSkipDir(name string) bool {
	if strings.HasPrefix(name, ".") && len(name) > 1 {
		// Skip dotted dirs by default unless explicitly worth keeping.
		// Common kept ones: .github
		if _, kept := keepDottedDirs[name]; kept {
			return false
		}
		if _, listed := skipDirs[name]; listed {
			return true
		}
		// Other dotted dirs (.venv, .terraform, etc.) -- skip.
		return true
	}
	_, skip := skipDirs[name]
	return skip
}

var keepDottedDirs = map[string]struct{}{
	".github": {},
}

func shouldSkipFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	_, skip := skipExts[ext]
	return skip
}

// searchTextResult is the canonical form: one match per "rel/path:line:text"
// string.
type searchTextResult []string

func runSearchText(ctx context.Context, root, query string, limit int) (searchTextResult, error) {
	if query == "" {
		return nil, fmt.Errorf("search query is empty")
	}
	if limit <= 0 {
		limit = 50
	}
	if rg := ripgrepPath(); rg != "" {
		out, err := runRipgrepText(ctx, rg, root, query, limit)
		if err == nil {
			return out, nil
		}
		// fall through to Go fallback on rg error -- e.g. rg refused to run
		// in the workspace for some reason; we still want best-effort results.
	}
	return runGoSearchText(ctx, root, query, limit)
}

func runRipgrepText(ctx context.Context, rg, root, query string, limit int) (searchTextResult, error) {
	// --line-number: emit "path:line:text"
	// --color=never: no ANSI escapes in the captured output
	// --no-heading: one match per line, with path on every line
	// --max-count: cap matches per file so a single huge file doesn't fill the limit
	// -- query: stop flag parsing so a query starting with "-" is treated as a literal
	args := []string{
		"--line-number",
		"--color=never",
		"--no-heading",
		"--max-count", fmt.Sprintf("%d", limit),
		"--", query, root,
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, rg, args...)
	cmd.Dir = root
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		// rg exits 1 when no matches were found -- treat as empty result.
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return nil, nil
		}
		return nil, fmt.Errorf("ripgrep failed: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	matches := make(searchTextResult, 0, 16)
	scanner := bufio.NewScanner(&stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		// rg with cmd.Dir=root gives absolute paths. Convert to relative.
		matches = append(matches, normalizeMatchPath(root, line))
		if len(matches) >= limit {
			break
		}
	}
	return matches, nil
}

func normalizeMatchPath(root, line string) string {
	// rg emits path:line:text; if path is absolute under root, rewrite to relative.
	idx := strings.Index(line, ":")
	if idx <= 0 {
		return line
	}
	path := line[:idx]
	rest := line[idx:]
	if !filepath.IsAbs(path) {
		return line
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return line
	}
	return filepath.ToSlash(rel) + rest
}

func runGoSearchText(ctx context.Context, root, query string, limit int) (searchTextResult, error) {
	matches := make(searchTextResult, 0, 16)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if len(matches) >= limit {
			return filepath.SkipAll
		}
		if d.IsDir() {
			if path == root {
				return nil
			}
			if shouldSkipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if shouldSkipFile(d.Name()) {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer file.Close()
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		lineNumber := 0
		for scanner.Scan() {
			lineNumber++
			text := scanner.Text()
			if strings.Contains(text, query) {
				rel, _ := filepath.Rel(root, path)
				matches = append(matches, fmt.Sprintf("%s:%d:%s", filepath.ToSlash(rel), lineNumber, text))
				if len(matches) >= limit {
					return filepath.SkipAll
				}
			}
		}
		return nil
	})
	if err != nil && err != filepath.SkipAll {
		return matches, err
	}
	return matches, nil
}

// runSearchFiles returns workspace-relative paths whose basename contains the
// (case-insensitive) substring `pattern`.
func runSearchFiles(ctx context.Context, root, pattern string) ([]string, error) {
	if pattern == "" {
		return nil, fmt.Errorf("search files pattern is empty")
	}
	if rg := ripgrepPath(); rg != "" {
		out, err := runRipgrepFiles(ctx, rg, root, pattern)
		if err == nil {
			return out, nil
		}
	}
	return runGoSearchFiles(ctx, root, pattern)
}

func runRipgrepFiles(ctx context.Context, rg, root, pattern string) ([]string, error) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, rg, "--files", root)
	cmd.Dir = root
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return nil, nil
		}
		return nil, fmt.Errorf("ripgrep --files failed: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	lower := strings.ToLower(pattern)
	matches := make([]string, 0, 16)
	scanner := bufio.NewScanner(&stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		path := scanner.Text()
		if !strings.Contains(strings.ToLower(filepath.Base(path)), lower) {
			continue
		}
		if filepath.IsAbs(path) {
			rel, err := filepath.Rel(root, path)
			if err == nil {
				path = rel
			}
		}
		matches = append(matches, filepath.ToSlash(path))
	}
	return matches, nil
}

func runGoSearchFiles(ctx context.Context, root, pattern string) ([]string, error) {
	lower := strings.ToLower(pattern)
	matches := make([]string, 0, 16)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			if path == root {
				return nil
			}
			if shouldSkipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.Contains(strings.ToLower(filepath.Base(path)), lower) {
			rel, _ := filepath.Rel(root, path)
			matches = append(matches, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return matches, err
	}
	return matches, nil
}
