package tui

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Suggest returns autocomplete suggestions for the current input.
func Suggest(input, cwd string) []string {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil
	}

	// Slash command completion.
	if strings.HasPrefix(input, "/") {
		var matches []string
		for _, cmd := range slashCommandNames() {
			if strings.HasPrefix(cmd, input) && cmd != input {
				matches = append(matches, cmd)
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
