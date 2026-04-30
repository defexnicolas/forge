package agent

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"forge/internal/tasks"
)

type executeTaskDispatchRequest struct {
	TaskID        string   `json:"task_id"`
	ID            string   `json:"id"`
	RelevantFiles []string `json:"relevant_files"`
	Notes         string   `json:"notes"`
	TargetFile    string   `json:"target_file"`
	FileStrategy  string   `json:"file_strategy"`
	SectionGoal   string   `json:"section_goal"`
}

type builderTaskGuard struct {
	TaskID         string
	Title          string
	Notes          string
	TargetFile     string
	FileStrategy   string
	SectionGoal    string
	AllowFullWrite bool
}

const (
	fileStrategyOneShotArtifact   = "one_shot_artifact"
	fileStrategyScaffoldThenPatch = "scaffold_then_patch"
)

var chunkableFileRe = regexp.MustCompile(`(?i)([A-Za-z0-9_./\\-]+\.(html|tsx|jsx|css|md|js))`)

// Implicit-language patterns: when the title mentions a language as a word
// rather than embedding a filename ("create snake game in html"), infer a
// sensible default filename. Without this, normalizeChecklistItems leaves
// the gigantic task intact and the Builder times out / loops.
var (
	implicitHTMLLangRe  = regexp.MustCompile(`(?i)\b(in|as|using)\s+(an?\s+)?html\b|\b(web\s*page|single[\s-]page\s+(app|application))\b`)
	implicitReactLangRe = regexp.MustCompile(`(?i)\b(in|as|using)\s+(an?\s+)?react\b|\breact\s+(component|app|page|ui)\b`)
	implicitJSXLangRe   = regexp.MustCompile(`(?i)\b(in|as|using)\s+jsx\b`)
	implicitJSLangRe    = regexp.MustCompile(`(?i)\b(in|as|using)\s+(an?\s+)?(vanilla\s+)?(js|javascript)\b`)
	implicitCSSLangRe   = regexp.MustCompile(`(?i)\b(in|as|using)\s+(an?\s+)?css\b|\bstylesheet\b`)
)

// complexDomainKeywords flags titles whose subject implies a non-trivial UI
// or app surface. Added 2026-04-30 so natural phrases like "snake game" force
// the Builder onto the scaffold_then_patch path even when the user did not
// type a filename literal in the title. Keep this list to multi-word phrases
// or unmistakable domains so "Create snake.js module" stays a one-shot task.
var complexDomainKeywords = []string{
	"snake game", "tetris", "pong", "platformer", "platform game",
	"card game", "puzzle game", "memory game", "match-3 game",
	"todo app", "todo list", "kanban", "calculator",
	"dashboard", "admin panel",
	"chat ui", "chat app", "messenger",
	"weather app", "quiz", "tracker", "pomodoro",
	"landing page", "portfolio site",
	"single page application", "single-page application",
}

func normalizeChecklistItems(items []string) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		if expanded := expandLargeFileTask(item); len(expanded) > 0 {
			out = append(out, expanded...)
			continue
		}
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func expandLargeFileTask(item string) []string {
	title := strings.TrimSpace(item)
	if title == "" {
		return nil
	}
	targetFile := inferChunkableTargetFile(title)
	if targetFile == "" || !looksLikeNewFileTask(title) || alreadyChunkedTask(title) {
		return nil
	}
	if inferDefaultFileStrategy(title, "") != fileStrategyScaffoldThenPatch {
		return nil
	}
	switch strings.ToLower(filepath.Ext(targetFile)) {
	case ".html":
		return []string{
			"Create " + targetFile + " scaffold",
			"Add head metadata to " + targetFile,
			"Add main layout to " + targetFile,
			"Add content sections to " + targetFile,
			"Add styles and polish to " + targetFile,
		}
	case ".tsx", ".jsx":
		return []string{
			"Create " + targetFile + " scaffold",
			"Wire state and data flow in " + targetFile,
			"Add markup sections to " + targetFile,
			"Add styling hooks in " + targetFile,
			"Verify behavior and polish in " + targetFile,
		}
	case ".css":
		return []string{
			"Create " + targetFile + " scaffold",
			"Add base rules to " + targetFile,
			"Add layout and component rules to " + targetFile,
			"Add responsive polish to " + targetFile,
		}
	case ".md":
		return []string{
			"Create " + targetFile + " outline",
			"Write opening sections in " + targetFile,
			"Write remaining sections in " + targetFile,
			"Polish examples and cleanup in " + targetFile,
		}
	default:
		return nil
	}
}

func deriveBuilderTaskGuard(task tasks.Task, req executeTaskDispatchRequest) builderTaskGuard {
	guard := builderTaskGuard{
		TaskID:       task.ID,
		Title:        task.Title,
		Notes:        task.Notes,
		TargetFile:   strings.TrimSpace(req.TargetFile),
		FileStrategy: strings.TrimSpace(req.FileStrategy),
		SectionGoal:  strings.TrimSpace(req.SectionGoal),
	}
	if guard.TargetFile == "" {
		guard.TargetFile = inferChunkableTargetFile(task.Title + "\n" + task.Notes)
	}
	if guard.FileStrategy == "" && guard.TargetFile != "" {
		if section := inferChunkedSectionGoal(task.Title); section != "" {
			guard.FileStrategy = fileStrategyScaffoldThenPatch
			if guard.SectionGoal == "" {
				guard.SectionGoal = section
			}
		} else {
			guard.FileStrategy = inferDefaultFileStrategy(task.Title, task.Notes)
		}
	}
	if guard.FileStrategy == fileStrategyOneShotArtifact {
		guard.AllowFullWrite = true
	}
	if guard.FileStrategy == fileStrategyScaffoldThenPatch && guard.SectionGoal == "scaffold" {
		guard.AllowFullWrite = true
	}
	return guard
}

func inferDefaultFileStrategy(title, notes string) string {
	text := strings.TrimSpace(title + "\n" + notes)
	if text == "" || !looksLikeNewFileTask(text) {
		return ""
	}
	if alreadyChunkedTask(title) || looksLikeLargeFileTask(text) {
		return fileStrategyScaffoldThenPatch
	}
	targetFile := inferChunkableTargetFile(text)
	if shouldPreferOneShotArtifact(targetFile) {
		return fileStrategyOneShotArtifact
	}
	return ""
}

func inferChunkableTargetFile(text string) string {
	if match := chunkableFileRe.FindStringSubmatch(text); len(match) >= 2 {
		return normalizeTaskPath(match[1])
	}
	return inferImplicitTargetFile(text)
}

// inferImplicitTargetFile derives a default filename when the title mentions
// a language as a word ("in html", "as a webpage", "in react") instead of
// embedding a filename. Returns "" when the title gives no language hint —
// callers treat that as "do not chunk".
func inferImplicitTargetFile(text string) string {
	s := strings.TrimSpace(text)
	if s == "" {
		return ""
	}
	// Order matters: react before js, since "in react" implies tsx not raw js.
	switch {
	case implicitReactLangRe.MatchString(s):
		return "App.tsx"
	case implicitJSXLangRe.MatchString(s):
		return "App.jsx"
	case implicitHTMLLangRe.MatchString(s):
		if slug := extractDomainSlug(s); slug != "" {
			return slug + ".html"
		}
		return "index.html"
	case implicitCSSLangRe.MatchString(s):
		// CSS rarely benefits from a domain-derived filename; "styles.css" is
		// the conventional default and avoids leaking unrelated noun phrases
		// (e.g. "draw something with a stylesheet" -> "styles.css").
		return "styles.css"
	case implicitJSLangRe.MatchString(s):
		if slug := extractDomainSlug(s); slug != "" {
			return slug + ".js"
		}
		return "app.js"
	}
	return ""
}

// extractDomainSlug pulls the noun phrase out of a "create X in Y" sentence
// and slugifies it. "create snake game in html" -> "snake-game".
func extractDomainSlug(text string) string {
	s := strings.ToLower(strings.TrimSpace(text))
	for _, verb := range []string{"create ", "build ", "implement ", "write ", "generate ", "make ", "add "} {
		if strings.HasPrefix(s, verb) {
			s = strings.TrimSpace(s[len(verb):])
			break
		}
	}
	for _, art := range []string{"a ", "an ", "the ", "some "} {
		if strings.HasPrefix(s, art) {
			s = strings.TrimSpace(s[len(art):])
			break
		}
	}
	for _, cut := range []string{" in ", " as ", " using "} {
		if idx := strings.Index(s, cut); idx > 0 {
			s = s[:idx]
			break
		}
	}
	return slugify(s)
}

func slugify(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevDash = false
		case r == ' ' || r == '-' || r == '_' || r == '.':
			if b.Len() > 0 && !prevDash {
				b.WriteRune('-')
				prevDash = true
			}
		}
	}
	return strings.TrimRight(b.String(), "-")
}

func normalizeTaskPath(path string) string {
	path = strings.TrimSpace(path)
	path = strings.Trim(path, "\"'`")
	path = strings.ReplaceAll(path, "\\", "/")
	return path
}

func inferChunkedSectionGoal(title string) string {
	lower := strings.ToLower(title)
	switch {
	case strings.Contains(lower, " scaffold"), strings.Contains(lower, " outline"):
		return "scaffold"
	case strings.Contains(lower, "head metadata"):
		return "head_metadata"
	case strings.Contains(lower, "main layout"):
		return "main_layout"
	case strings.Contains(lower, "content sections"):
		return "content_sections"
	case strings.Contains(lower, "styles and polish"), strings.Contains(lower, "responsive polish"):
		return "styles_polish"
	case strings.Contains(lower, "state and data flow"):
		return "state_data"
	case strings.Contains(lower, "markup sections"):
		return "markup_sections"
	case strings.Contains(lower, "styling hooks"):
		return "styling_hooks"
	case strings.Contains(lower, "verify behavior and polish"), strings.Contains(lower, "polish examples and cleanup"):
		return "polish"
	case strings.Contains(lower, "opening sections"):
		return "opening_sections"
	case strings.Contains(lower, "remaining sections"):
		return "remaining_sections"
	default:
		return ""
	}
}

func alreadyChunkedTask(title string) bool {
	return inferChunkedSectionGoal(title) != ""
}

func looksLikeLargeFileTask(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == "" {
		return false
	}
	for _, needle := range []string{
		"multi-section",
		"multiple sections",
		"section-by-section",
		"section by section",
		"comprehensive",
		"detailed",
		"full documentation",
		"long-form",
		"large page",
		"large document",
	} {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	for _, needle := range []string{
		"readme.md",
		"docs/",
		"documentation",
		"developer guide",
		"user guide",
		"reference doc",
		"architecture doc",
	} {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	for _, needle := range complexDomainKeywords {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

func shouldPreferOneShotArtifact(path string) bool {
	switch strings.ToLower(filepath.Ext(normalizeTaskPath(path))) {
	case ".html", ".css", ".js":
		return true
	default:
		return false
	}
}

func looksLikeNewFileTask(title string) bool {
	lower := strings.ToLower(title)
	for _, verb := range []string{"create ", "build ", "implement ", "write ", "generate ", "make "} {
		if strings.Contains(lower, verb) {
			return true
		}
	}
	return false
}

func (r *Runtime) setActiveBuilderTask(guard *builderTaskGuard) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if guard == nil {
		r.activeBuilderTask = nil
		return
	}
	copy := *guard
	r.activeBuilderTask = &copy
}

func (r *Runtime) currentBuilderTask() (builderTaskGuard, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.activeBuilderTask == nil {
		return builderTaskGuard{}, false
	}
	return *r.activeBuilderTask, true
}

func (r *Runtime) validateWriteFileMutation(path, content string) error {
	guard, ok := r.currentBuilderTask()
	if !ok || guard.FileStrategy != fileStrategyScaffoldThenPatch || !sameTaskTarget(guard.TargetFile, path) {
		return nil
	}
	if guard.AllowFullWrite {
		if isOversizedScaffoldContent(content) {
			return fmt.Errorf("do not create the full file in one shot for %s — this scaffold task must only write a minimal skeleton, then continue with edit_file or apply_patch", path)
		}
		return nil
	}
	section := guard.SectionGoal
	if section == "" {
		section = "current section"
	}
	return fmt.Errorf("do not create the full file in one shot for %s — this task is part of scaffold_then_patch (%s). Use edit_file or apply_patch to change only this section", path, section)
}

func sameTaskTarget(a, b string) bool {
	return strings.EqualFold(normalizeTaskPath(a), normalizeTaskPath(b))
}

func isOversizedScaffoldContent(content string) bool {
	if len(content) > 1400 {
		return true
	}
	if strings.Count(content, "\n")+1 > 80 {
		return true
	}
	return false
}
