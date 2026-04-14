package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// ServerConfig describes one MCP server entry in .mcp.json.
type ServerConfig struct {
	// stdio transport fields.
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	// SSE/HTTP transport fields.
	Transport string `json:"transport"` // "stdio" (default), "sse", "http"
	URL       string `json:"url"`       // for sse/http transport
}

// Config is the top-level structure of .mcp.json.
type Config struct {
	MCPServers map[string]ServerConfig `json:"mcpServers"`
}

// LoadConfig reads .mcp.json from the given directory.
// If the file does not exist, an empty config is returned with no error.
func LoadConfig(cwd string) (Config, error) {
	path := filepath.Join(cwd, ".mcp.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, nil
		}
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	if cfg.MCPServers == nil {
		cfg.MCPServers = map[string]ServerConfig{}
	}
	return cfg, nil
}
