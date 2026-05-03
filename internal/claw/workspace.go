package claw

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"text/template"
	"time"

	"forge/internal/globalconfig"
)

// clawWorkspaceDir is the on-disk home for the markdown personality
// files Claw reads each turn. Lives under the global Claw home so it
// survives workspace switches and Hub-only sessions.
//
// Files inside (each one a thin section the LLM reads as part of its
// system prompt):
//
//   AGENTS.md     — behavioural rules / how Claw should operate
//   IDENTITY.md   — name, vibe, emoji, short description
//   SOUL.md       — personality, values, communication style
//   USER.md       — about the human Claw is paired with
//   TOOLS.md      — local-specific notes (contacts, hosts, etc.)
//   HEARTBEAT.md  — operator-edited periodic checklist
//   MEMORY.md     — curated long-term memory; main session only
//
// The bootstrap function only writes templates the first time the
// folder is missing. Subsequent edits the user makes are preserved —
// the loader reads fresh each turn.
const clawWorkspaceDirName = "workspace"

func clawWorkspacePath() string {
	return filepath.Join(globalconfig.HomeDir(), "claw", clawWorkspaceDirName)
}

// bootstrapClawWorkspace writes the seven personality files when the
// workspace folder doesn't exist yet. Uses templates seeded from the
// current state so users who already have an interview-derived
// Identity/Soul/User see their data carried forward into prose.
//
// Idempotent: after the folder exists, returns without touching the
// files. The user is free to edit them with any text editor.
func bootstrapClawWorkspace(state State) error {
	dir := clawWorkspacePath()
	if _, err := os.Stat(dir); err == nil {
		// Already bootstrapped — preserve user edits.
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("workspace mkdir: %w", err)
	}
	files := renderClawWorkspaceTemplates(state)
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return fmt.Errorf("workspace write %s: %w", name, err)
		}
	}
	fmt.Fprintf(os.Stderr, "[claw] workspace bootstrapped at %s\n", dir)
	return nil
}

// regenerateClawWorkspaceFiles overwrites IDENTITY.md, SOUL.md, USER.md
// from the current state. Called from applyInterviewUpdates so that
// interview-driven changes propagate to the markdown layer the next
// turn reads. Other files (AGENTS, TOOLS, HEARTBEAT, MEMORY) are NOT
// touched here — they're either static behaviour rules or
// user-curated content that the interview can't safely overwrite.
func regenerateClawWorkspaceFiles(state State) {
	dir := clawWorkspacePath()
	if _, err := os.Stat(dir); err != nil {
		// Workspace not bootstrapped yet — nothing to regenerate.
		return
	}
	files := renderClawWorkspaceTemplates(state)
	for _, name := range []string{"IDENTITY.md", "SOUL.md", "USER.md"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(files[name]), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "[claw] regenerate %s failed: %v\n", name, err)
		}
	}
}

// renderClawWorkspaceTemplates expands the seven embedded templates
// against the current state and returns a name→content map. Pure
// function — no I/O. The bootstrap and regeneration paths share this.
func renderClawWorkspaceTemplates(state State) map[string]string {
	data := workspaceTemplateData{
		Name:         firstNonEmpty(state.Identity.Name, "Claw"),
		Tone:         firstNonEmpty(state.Identity.Tone, "warm, direct"),
		Style:        firstNonEmpty(state.Identity.Style, "concise"),
		Description:  state.Identity.Description,
		Seed:         firstNonEmpty(state.Identity.Seed, "A resident Forge companion with memory, initiative, and restraint."),
		Values:       state.Soul.Values,
		Goals:        state.Soul.Goals,
		Traits:       state.Soul.Traits,
		LearnedNotes: state.Soul.LearnedNotes,
		UserName:     firstNonEmpty(state.User.DisplayName, "(unknown)"),
		Timezone:     firstNonEmpty(state.User.Timezone, "(unset)"),
		Language:     normalizeLanguageValue(state.User.Preferences["preferred_language"]),
		Preferences:  sortedPrefs(state.User.Preferences),
		Contacts:     sortedContacts(state.Contacts),
		Summaries:    recentSummaries(state.Memory.Summaries, 8),
	}
	out := map[string]string{}
	for name, tpl := range workspaceTemplates {
		var buf bytes.Buffer
		if err := tpl.Execute(&buf, data); err != nil {
			out[name] = "<!-- template render failed: " + err.Error() + " -->"
			continue
		}
		out[name] = buf.String()
	}
	return out
}

type workspaceContact struct {
	Name, Phone, Email, Notes string
}

type workspaceTemplateData struct {
	Name         string
	Tone         string
	Style        string
	Description  string
	Seed         string
	Values       []string
	Goals        []string
	Traits       []string
	LearnedNotes []string
	UserName     string
	Timezone     string
	Language     string
	Preferences  []workspacePref
	Contacts     []workspaceContact
	Summaries    []string
}

type workspacePref struct {
	Key, Value string
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}

func sortedPrefs(prefs map[string]string) []workspacePref {
	if len(prefs) == 0 {
		return nil
	}
	keys := make([]string, 0, len(prefs))
	for k := range prefs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]workspacePref, 0, len(keys))
	for _, k := range keys {
		out = append(out, workspacePref{Key: k, Value: prefs[k]})
	}
	return out
}

func sortedContacts(contacts map[string]Contact) []workspaceContact {
	if len(contacts) == 0 {
		return nil
	}
	keys := make([]string, 0, len(contacts))
	for k := range contacts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]workspaceContact, 0, len(keys))
	for _, k := range keys {
		c := contacts[k]
		out = append(out, workspaceContact{
			Name:  c.Name,
			Phone: c.Phone,
			Email: c.Email,
			Notes: c.Notes,
		})
	}
	return out
}

func recentSummaries(items []MemorySummary, max int) []string {
	if len(items) == 0 {
		return nil
	}
	if max <= 0 {
		max = 8
	}
	start := len(items) - max
	if start < 0 {
		start = 0
	}
	out := make([]string, 0, len(items)-start)
	for i := start; i < len(items); i++ {
		s := strings.TrimSpace(items[i].Summary)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// workspaceTemplates is the static template registry. Compiled once
// at package init so the bootstrap path is allocation-light.
var workspaceTemplates = func() map[string]*template.Template {
	tmpls := map[string]string{
		"AGENTS.md":    agentsTemplate,
		"IDENTITY.md":  identityTemplate,
		"SOUL.md":      soulTemplate,
		"USER.md":      userTemplate,
		"TOOLS.md":     toolsTemplate,
		"HEARTBEAT.md": heartbeatTemplate,
		"MEMORY.md":    memoryTemplate,
	}
	out := make(map[string]*template.Template, len(tmpls))
	for name, body := range tmpls {
		out[name] = template.Must(template.New(name).Parse(body))
	}
	return out
}()

const agentsTemplate = `# AGENTS.md — How I Operate

This file teaches me how to behave. The user can edit it freely; I read it fresh every turn.

## Core posture

- Be **proactive but not pushy**. If I see something useful, I act on internal things (read, organize, look up). For external actions (sending messages, posting), I tell the user first.
- Be **direct**. No "great question!", no filler. Get to the point.
- Be **brief**. WhatsApp replies = 1-2 sentences. The TUI chat can be a bit longer when it helps.
- Match the user's **tone and language** automatically.

## Memory

- Long-term memory lives in MEMORY.md. I read it only in main sessions (the TUI or messages from the owner).
- The structured state.json holds threads, contacts, reminders, and facts. I use the dedicated tools to touch those.
- "Mental notes" don't survive — if it matters, write it down (state, MEMORY.md, or a fact via claw_remember).

## When to use tools

- ` + "`web_search` / `web_fetch`" + ` only when the user's question depends on info I don't already know (recent news, live data).
- ` + "`whatsapp_send`" + ` only when the user explicitly asks me to message a contact AND gives the recipient. Never speculate a number.
- ` + "`claw_save_contact`" + ` when the user asks me to "agendar", "guardar", or "remember" a person.
- ` + "`claw_remember` / `claw_recall`" + ` for free-form facts (allergies, preferences, recurring details).
- ` + "`claw_schedule_reminder`" + ` when the user asks to be reminded later. Convert relative phrasings into ISO 8601 timestamps before calling.

## Boundaries

- **Internal** (read, organize, look up, think): free, no permission needed.
- **External** (send messages, post publicly): tell the user first, get green light.
- **Private data**: stays with me. I don't repeat the user's stuff to anyone else.
- **Group chats / shared contexts**: I don't load MEMORY.md and I'm conservative about what I share.

## Red lines

- No exfiltrating private data. Ever.
- No destructive commands without asking.
- Prefer recoverable operations.
- When in doubt, ask.
`

const identityTemplate = `# IDENTITY.md — Who I Am

- **Name:** {{.Name}}
- **Tone:** {{.Tone}}
- **Style:** {{.Style}}
{{- if .Description}}
- **Description:** {{.Description}}
{{- end}}

---

_This file is mine. Edit it as I learn more about who I am._
`

const soulTemplate = `# SOUL.md — How I Show Up

Tone: **{{.Tone}}** · Style: **{{.Style}}**
{{if .Seed}}
> {{.Seed}}
{{end}}
{{if .Values}}
## What I value

{{range .Values}}- {{.}}
{{end}}{{end}}
{{- if .Goals}}
## What I optimise for

{{range .Goals}}- {{.}}
{{end}}{{end}}
{{- if .Traits}}
## Traits

{{range .Traits}}- {{.}}
{{end}}{{end}}
{{- if .LearnedNotes}}
## Notes I've learned along the way

{{range .LearnedNotes}}- {{.}}
{{end}}{{end}}

---

_This file is mine. I update it as I learn more about how we work together._
`

const userTemplate = `# USER.md — About My Human

- **Name:** {{.UserName}}
- **Timezone:** {{.Timezone}}
{{- if .Language}}
- **Preferred language:** {{.Language}}
{{- end}}
{{if .Preferences}}
## Preferences

{{range .Preferences}}- **{{.Key}}**: {{.Value}}
{{end}}{{end}}
---

_What I learn about my human goes here. I keep it factual and respectful._
`

const toolsTemplate = `# TOOLS.md — Local Notes

Skills define _how_ tools work. This file is for _my_ specifics — the stuff that's unique to this setup.

{{if .Contacts}}## Contacts

{{range .Contacts}}- **{{.Name}}**{{if .Phone}} → {{.Phone}}{{end}}{{if .Email}} ({{.Email}}){{end}}{{if .Notes}} — _{{.Notes}}_{{end}}
{{end}}{{else}}## Contacts

_No saved contacts yet. Use ` + "`claw_save_contact`" + ` from the chat or the ` + "`/contacts`" + ` slash command._
{{end}}
## Conventions

- Default channel: WhatsApp.
- Default language: matches USER.md.
- Reminder pump ticks every 30s; reminders fire as soon as their RemindAt passes.

---

_Add anything environment-specific here: SSH hosts, camera names, voice preferences. Skills stay shared; this stays mine._
`

const heartbeatTemplate = `# HEARTBEAT.md — My Periodic Checklist

<!-- Empty by default. Add bullets I should check periodically and I'll
batch them when the heartbeat ticks. Examples:

- Inbox: any urgent unread email?
- Calendar: anything in the next 2 hours?
- Reminders: any pending reminders that need a follow-up?
-->
`

const memoryTemplate = `# MEMORY.md — My Long-Term Memory

> ⚠️ I only load this in **main sessions** (TUI chat or messages from the owner). It never travels into shared contexts or group chats.

This is my curated, distilled memory — not raw logs. Things I've decided to keep:

{{if .Summaries}}{{range .Summaries}}- {{.}}
{{end}}{{else}}_(no curated memories yet)_
{{end}}
---

_I add to this file when something matters enough to remember beyond a single conversation._

_Last bootstrap: {{template "_now" .}}_
{{define "_now"}}` + "`" + `bootstrap` + "`" + `{{end}}
`

// allowedWorkspaceNoteFiles is the set of markdown files Claw is
// allowed to append notes to via claw_workspace_note. AGENTS.md and
// HEARTBEAT.md are intentionally excluded — AGENTS is the constitution
// (operator-edited), HEARTBEAT is the operator's checklist.
var allowedWorkspaceNoteFiles = map[string]bool{
	"MEMORY.md":   true,
	"SOUL.md":     true,
	"USER.md":     true,
	"TOOLS.md":    true,
	"IDENTITY.md": true,
}

// canonicalWorkspaceFileName accepts loose names (memory, MEMORY,
// memory.md, MEMORY.md) and returns the canonical "MEMORY.md" form,
// or "" if not recognized.
func canonicalWorkspaceFileName(name string) string {
	t := strings.ToUpper(strings.TrimSpace(name))
	if t == "" {
		return ""
	}
	if !strings.HasSuffix(t, ".MD") {
		t += ".MD"
	}
	// Match against allowed set with case-insensitive .md suffix.
	for canonical := range allowedWorkspaceNoteFiles {
		if strings.EqualFold(canonical, t) {
			return canonical
		}
	}
	return ""
}

// appendToWorkspaceFile appends a single bullet/note line to the named
// file in the workspace folder. Idempotent: if the same trimmed line
// already exists, the file is not modified. Returns nil silently when
// the workspace folder doesn't exist (i.e., bootstrap hasn't run yet)
// — callers don't need to gate.
//
// `name` must be one of allowedWorkspaceNoteFiles (canonical form).
// Use canonicalWorkspaceFileName to normalise loose input before
// calling.
func appendToWorkspaceFile(name, line string) error {
	if !allowedWorkspaceNoteFiles[name] {
		return fmt.Errorf("workspace file %q not allowed for notes", name)
	}
	dir := clawWorkspacePath()
	if _, err := os.Stat(dir); err != nil {
		// Workspace not bootstrapped — nothing to append to. Silent
		// success because callers shouldn't need to know.
		return nil
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return fmt.Errorf("empty note line")
	}
	path := filepath.Join(dir, name)
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	// Idempotency: bail out if the exact line is already there.
	// Matches whether the line is bullet-prefixed or not, so callers
	// can pass either "- text" or "text".
	body := string(existing)
	probes := []string{line, "- " + line, line + "\n"}
	for _, probe := range probes {
		if strings.Contains(body, probe) {
			return nil
		}
	}
	// Ensure the file ends with a newline before appending.
	if len(body) > 0 && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	// Add a leading bullet only if the caller didn't include one.
	out := line
	if !strings.HasPrefix(out, "- ") && !strings.HasPrefix(out, "* ") {
		out = "- " + out
	}
	body += out + "\n"
	// Invalidate the cache so the next loadClawWorkspace re-reads.
	clawWorkspaceCache.mu.Lock()
	clawWorkspaceCache.includeM = map[bool]workspaceCacheEntry{}
	clawWorkspaceCache.mu.Unlock()
	return os.WriteFile(path, []byte(body), 0o644)
}

// loadedClawWorkspace caches the most recent read of the workspace
// folder, keyed by include-memory mode (so we don't reuse a "no
// memory" cache when memory is requested).
type loadedClawWorkspace struct {
	mu       sync.Mutex
	includeM map[bool]workspaceCacheEntry
}

type workspaceCacheEntry struct {
	files     ClawWorkspaceFiles
	mtimes    map[string]time.Time
	cachedAt  time.Time
}

var clawWorkspaceCache = &loadedClawWorkspace{
	includeM: map[bool]workspaceCacheEntry{},
}

// ClawWorkspaceFiles is the loaded content of the workspace markdown
// folder. Empty fields mean the file was missing or unreadable —
// callers compose system prompts that tolerate any combination.
type ClawWorkspaceFiles struct {
	Agents    string
	Identity  string
	Soul      string
	User      string
	Tools     string
	Heartbeat string
	Memory    string
}

// loadClawWorkspace reads the seven .md files from disk and returns
// their content. When includeMemory is false (shared contexts:
// messages from non-owner contacts), MEMORY.md is skipped entirely
// — the function won't even open the file. Cache uses mtime so manual
// edits between turns are picked up without restart.
func loadClawWorkspace(includeMemory bool) ClawWorkspaceFiles {
	dir := clawWorkspacePath()
	files := []string{"AGENTS.md", "IDENTITY.md", "SOUL.md", "USER.md", "TOOLS.md", "HEARTBEAT.md"}
	if includeMemory {
		files = append(files, "MEMORY.md")
	}

	// Check cache first.
	clawWorkspaceCache.mu.Lock()
	cached, ok := clawWorkspaceCache.includeM[includeMemory]
	clawWorkspaceCache.mu.Unlock()
	if ok {
		stale := false
		for _, name := range files {
			info, err := os.Stat(filepath.Join(dir, name))
			if err != nil {
				if !cached.mtimes[name].IsZero() {
					stale = true
					break
				}
				continue
			}
			if info.ModTime() != cached.mtimes[name] {
				stale = true
				break
			}
		}
		if !stale {
			return cached.files
		}
	}

	out := ClawWorkspaceFiles{}
	mtimes := map[string]time.Time{}
	for _, name := range files {
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		mtimes[name] = info.ModTime()
		body, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		assignWorkspaceField(&out, name, string(body))
	}

	clawWorkspaceCache.mu.Lock()
	clawWorkspaceCache.includeM[includeMemory] = workspaceCacheEntry{
		files:    out,
		mtimes:   mtimes,
		cachedAt: time.Now(),
	}
	clawWorkspaceCache.mu.Unlock()
	return out
}

func assignWorkspaceField(out *ClawWorkspaceFiles, name, content string) {
	switch name {
	case "AGENTS.md":
		out.Agents = content
	case "IDENTITY.md":
		out.Identity = content
	case "SOUL.md":
		out.Soul = content
	case "USER.md":
		out.User = content
	case "TOOLS.md":
		out.Tools = content
	case "HEARTBEAT.md":
		out.Heartbeat = content
	case "MEMORY.md":
		out.Memory = content
	}
}

// composeClawSystemPrompt assembles the markdown sections into the
// final system prompt content. Each non-empty file appears as its own
// labelled block so the LLM can attribute behaviour to a specific
// section. Empty sections are omitted to keep the prompt tight.
func composeClawSystemPrompt(files ClawWorkspaceFiles) string {
	var b strings.Builder
	appendSection := func(title, body string) {
		body = strings.TrimSpace(body)
		if body == "" {
			return
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("=== ")
		b.WriteString(title)
		b.WriteString(" ===\n")
		b.WriteString(body)
	}
	appendSection("AGENTS", files.Agents)
	appendSection("IDENTITY", files.Identity)
	appendSection("SOUL", files.Soul)
	appendSection("USER", files.User)
	appendSection("TOOLS", files.Tools)
	appendSection("HEARTBEAT", files.Heartbeat)
	appendSection("MEMORY", files.Memory)
	return b.String()
}
