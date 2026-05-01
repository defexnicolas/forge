package tui

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func compactDisplayPath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	return compactDisplayPathFromHome(path, home)
}

func compactDisplayPathFromHome(path, home string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	cleanPath := filepath.Clean(path)
	cleanHome := filepath.Clean(home)
	if cleanHome != "" && hasPathPrefix(cleanPath, cleanHome) {
		rest := strings.TrimPrefix(cleanPath, cleanHome)
		if rest == "" {
			return "~"
		}
		if strings.HasPrefix(rest, string(filepath.Separator)) {
			return "~" + rest
		}
		return "~" + string(filepath.Separator) + rest
	}
	return cleanPath
}

func hasPathPrefix(path, prefix string) bool {
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
		prefix = strings.ToLower(prefix)
	}
	if path == prefix {
		return true
	}
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	if len(path) <= len(prefix) {
		return true
	}
	return path[len(prefix)] == filepath.Separator
}
