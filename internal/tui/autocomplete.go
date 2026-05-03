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
