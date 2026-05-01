package lsp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// fileConfig is the on-disk shape of a .lsp.json file.
//
//	{
//	  "servers": {
//	    "go": {
//	      "command": "gopls",
//	      "args": [],
//	      "language": "go",
//	      "extensions": [".go"]
//	    },
//	    "typescript": {
//	      "command": "typescript-language-server",
//	      "args": ["--stdio"],
//	      "language": "typescript",
//	      "extensions": [".ts", ".tsx", ".js", ".jsx"]
//	    }
//	  }
//	}
type fileConfig struct {
	Servers map[string]extendedServerConfig `json:"servers"`
}

type extendedServerConfig struct {
	Command    string   `json:"command"`
	Args       []string `json:"args"`
	Language   string   `json:"language"`
	Extensions []string `json:"extensions"`
}

// Config holds the merged language-server config: extension -> ServerConfig.
type Config struct {
	// ByExt maps lowercased file extensions (".go", ".ts", ...) to the
	// ServerConfig that should be used for files of that extension. Multiple
	// extensions can point at the same server.
	ByExt map[string]ServerConfig
}

// LoadConfig reads .lsp.json files from the project (`<cwd>/.lsp.json`) and
// from each path in `extra` (typically plugin-shipped configs). Later files
// override earlier ones for the same extension, so plugins can extend but
// the project file always wins for the extensions it declares.
//
// A missing file is not an error -- LoadConfig returns an empty config. A
// malformed file is an error so the user notices the typo.
func LoadConfig(cwd string, extra ...string) (Config, error) {
	cfg := Config{ByExt: map[string]ServerConfig{}}

	// Plugins first, project last, so the project wins.
	paths := append([]string{}, extra...)
	paths = append(paths, filepath.Join(cwd, ".lsp.json"))

	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return cfg, fmt.Errorf("read %s: %w", path, err)
		}
		var fc fileConfig
		if err := json.Unmarshal(data, &fc); err != nil {
			return cfg, fmt.Errorf("parse %s: %w", path, err)
		}
		for _, srv := range fc.Servers {
			if srv.Command == "" {
				continue
			}
			base := ServerConfig{
				Command:  srv.Command,
				Args:     srv.Args,
				Language: srv.Language,
			}
			for _, ext := range srv.Extensions {
				ext = strings.ToLower(strings.TrimSpace(ext))
				if ext == "" {
					continue
				}
				if !strings.HasPrefix(ext, ".") {
					ext = "." + ext
				}
				cfg.ByExt[ext] = base
			}
		}
	}
	return cfg, nil
}

// ResolveForFile picks the server appropriate for a given file path, or
// returns ok=false when no entry matches the file's extension.
func (c Config) ResolveForFile(path string) (ServerConfig, bool) {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == "" {
		return ServerConfig{}, false
	}
	srv, ok := c.ByExt[ext]
	return srv, ok
}
