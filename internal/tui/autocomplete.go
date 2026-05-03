package tui

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"forge/internal/plugins"
)

// pluginCommandSuggestions enumerates /<plugin>:<command> entries by
// running the same Discover() the dispatcher uses. cwd is the workspace
// root (or "" for hub-only). Failures are silently ignored so a broken
// plugin cannot block autocomplete on the rest of the catalog.
func pluginCommandSuggestions(cwd string) []string {
	if strings.TrimSpace(cwd) == "" {
		return nil
	}
	mgr := plugins.NewManager(cwd)
	discovered, err := mgr.Discover()
	if err != nil {
		return nil
	}
	var out []string
	for _, p := range discovered {
		for _, c := range plugins.LoadCommands(p.Path) {
			out = append(out, "/"+p.Name+":"+c.Name)
		}
	}
	return out
}

// skillCommandSuggestions enumerates /<skill-name> entries by scanning
// the same dirs runSkillTool.searchDirs uses (workspace + home), so the
// autocomplete set matches what dispatchSkillCommand can actually
// resolve. Skills hidden by a built-in (e.g. /review) are filtered by
// the caller in Suggest, not here.
func skillCommandSuggestions(cwd string) []string {
	var dirs []string
	if strings.TrimSpace(cwd) != "" {
		dirs = append(dirs,
			filepath.Join(cwd, ".agents", "skills"),
			filepath.Join(cwd, ".forge", "skills"),
		)
	}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs,
			filepath.Join(home, ".codex", "skills"),
			filepath.Join(home, ".forge", "skills"),
			// Claude Code-style location — scanned last so a same-named
			// forge/codex skill wins. Mirrors the order in
			// internal/tools/skill.go and internal/skills/manager.go.
			filepath.Join(home, ".claude", "skills"),
		)
	}
	seen := map[string]bool{}
	var out []string
	for _, d := range dirs {
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			name := e.Name()
			if seen[name] {
				continue
			}
			if _, err := os.Stat(filepath.Join(d, name, "SKILL.md")); err != nil {
				continue
			}
			seen[name] = true
			out = append(out, "/"+name)
		}
	}
	sort.Strings(out)
	return out
}

// Suggest returns autocomplete suggestions for the current input.
func Suggest(input, cwd string) []string {
	if strings.TrimSpace(input) == "" {
		return nil
	}
	input = strings.TrimLeft(input, " \t\r\n")

	// Slash command completion.
	if strings.HasPrefix(input, "/") {
		// Subcommand completion when a space is present.
		if spaceIdx := strings.Index(input, " "); spaceIdx >= 0 {
			head := input[:spaceIdx]
			rest := strings.TrimLeft(input[spaceIdx+1:], " ")
			subs := subcommandsFor(head)
			if len(subs) == 0 {
				return nil
			}
			var matches []string
			for _, sub := range subs {
				full := head + " " + sub
				if strings.HasPrefix(sub, rest) && full != input {
					matches = append(matches, full)
				}
			}
			return matches
		}
		var matches []string
		for _, cmd := range slashCommandNames() {
			if strings.HasPrefix(cmd, input) && cmd != input {
				matches = append(matches, cmd)
			}
		}
		// Plugin commands (/<plugin>:<command>) are discovered on the fly
		// from the workspace + global plugin dirs. Treat them as additive
		// suggestions so the static autocomplete tests keep passing.
		for _, cmd := range pluginCommandSuggestions(cwd) {
			if strings.HasPrefix(cmd, input) && cmd != input {
				matches = append(matches, cmd)
			}
		}
		// Skill commands (/<skill-name>) — additive same as plugins.
		// Built-in names always shadow a same-named skill in the
		// dispatcher, so filter them out of completions to avoid the
		// user thinking the skill is reachable.
		builtins := map[string]bool{}
		for _, c := range slashCommandNames() {
			builtins[c] = true
		}
		for _, cmd := range skillCommandSuggestions(cwd) {
			if builtins[cmd] {
				continue
			}
			if strings.HasPrefix(cmd, input) && cmd != input {
				matches = append(matches, cmd)
			}
		}
		// If the input is an exact command name, surface its subcommands as
		// the next completion step so the user discovers them without having
		// to type a trailing space first.
		if len(matches) == 0 {
			if subs := subcommandsFor(input); len(subs) > 0 {
				for _, sub := range subs {
					matches = append(matches, input+" "+sub)
				}
			}
		}
		return matches
	}

	// @ mention file completion.
	atIdx := strings.LastIndex(input, "@")
	if atIdx >= 0 {
		partial := input[atIdx+1:]
		return completeFiles(cwd, partial)
	}

	return nil
}

func completeFiles(cwd, partial string) []string {
	dir := cwd
	prefix := ""
	if idx := strings.LastIndex(partial, "/"); idx >= 0 {
		dir = filepath.Join(cwd, partial[:idx])
		prefix = partial[:idx+1]
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	lower := strings.ToLower(partial)
	baseLower := strings.ToLower(filepath.Base(partial))
	var matches []string
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		fullPath := prefix + name
		if entry.IsDir() {
			fullPath += "/"
		}
		if strings.HasPrefix(strings.ToLower(fullPath), lower) || strings.HasPrefix(strings.ToLower(name), baseLower) {
			matches = append(matches, "@"+fullPath)
		}
	}
	sort.Strings(matches)
	if len(matches) > 8 {
		matches = matches[:8]
	}
	return matches
}
