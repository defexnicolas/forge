package mcp

import (
	"os"
	"path/filepath"
	"testing"
)

func TestToolName(t *testing.T) {
	tests := []struct {
		server string
		tool   string
		want   string
	}{
		{"filesystem", "read_file", "mcp_filesystem_read_file"},
		{"github", "list_repos", "mcp_github_list_repos"},
		{"db", "query", "mcp_db_query"},
	}
	for _, tt := range tests {
		got := ToolName(tt.server, tt.tool)
		if got != tt.want {
			t.Errorf("ToolName(%q, %q) = %q, want %q", tt.server, tt.tool, got, tt.want)
		}
	}
}

func TestLoadConfigMissing(t *testing.T) {
	dir := t.TempDir()
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig on empty dir: %v", err)
	}
	if len(cfg.MCPServers) != 0 {
		t.Errorf("expected empty MCPServers, got %d", len(cfg.MCPServers))
	}
}

func TestLoadConfigValid(t *testing.T) {
	dir := t.TempDir()
	content := `{
  "mcpServers": {
    "filesystem": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"],
      "env": {"DEBUG": "1"}
    },
    "github": {
      "command": "gh-mcp"
    }
  }
}`
	err := os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte(content), 0644)
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(cfg.MCPServers) != 2 {
		t.Fatalf("expected 2 servers, got %d", len(cfg.MCPServers))
	}

	fs := cfg.MCPServers["filesystem"]
	if fs.Command != "npx" {
		t.Errorf("filesystem command = %q, want npx", fs.Command)
	}
	if len(fs.Args) != 3 {
		t.Errorf("filesystem args len = %d, want 3", len(fs.Args))
	}
	if fs.Env["DEBUG"] != "1" {
		t.Errorf("filesystem env DEBUG = %q, want 1", fs.Env["DEBUG"])
	}

	gh := cfg.MCPServers["github"]
	if gh.Command != "gh-mcp" {
		t.Errorf("github command = %q, want gh-mcp", gh.Command)
	}
}

func TestLoadConfigInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte(`{invalid`), 0644)
	if err != nil {
		t.Fatal(err)
	}
	_, err = LoadConfig(dir)
	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

func TestMcpToolInterface(t *testing.T) {
	// Verify mcpTool satisfies tools.Tool at compile time.
	tool := &mcpTool{
		serverName: "test",
		info: mcpToolInfo{
			Name:        "hello",
			Description: "say hello",
			InputSchema: []byte(`{"type":"object","properties":{"name":{"type":"string"}}}`),
		},
	}
	if tool.Name() != "mcp_test_hello" {
		t.Errorf("Name() = %q, want mcp_test_hello", tool.Name())
	}
	if tool.Description() != "say hello" {
		t.Errorf("Description() = %q, want 'say hello'", tool.Description())
	}
	schema := string(tool.Schema())
	if schema == "" || schema == "null" {
		t.Error("Schema() should not be empty")
	}
}
