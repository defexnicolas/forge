package plugins

import (
	"os"
	"path/filepath"
	"strings"
)

// Command represents a slash command loaded from a plugin.
type Command struct {
	Name    string
	Content string // raw markdown content
	Source  string // plugin name
}

// AgentDef represents a subagent definition from a plugin.
type AgentDef struct {
	Name        string
	Content     string
	Source      string
	Description string
}

// LoadCommands reads all *.md files from a plugin's commands/ directory.
func LoadCommands(pluginPath string) []Command {
	dir := filepath.Join(pluginPath, "commands")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var commands []Command
	pluginName := filepath.Base(pluginPath)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".md")
		commands = append(commands, Command{
			Name:    name,
			Content: string(data),
			Source:  pluginName,
		})
	}
	return commands
}

// LoadAgents reads all *.md files from a plugin's agents/ directory.
func LoadAgents(pluginPath string) []AgentDef {
	dir := filepath.Join(pluginPath, "agents")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var agents []AgentDef
	pluginName := filepath.Base(pluginPath)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".md")
		content := string(data)
		desc := extractFirstLine(content)
		agents = append(agents, AgentDef{
			Name:        name,
			Content:     content,
			Source:      pluginName,
			Description: desc,
		})
	}
	return agents
}

// ExecuteCommand returns the markdown content for a named command.
// This can be injected as system prompt context.
func (m *Manager) ExecuteCommand(name string) (string, error) {
	plugins, err := m.Discover()
	if err != nil {
		return "", err
	}
	for _, plugin := range plugins {
		for _, cmd := range LoadCommands(plugin.Path) {
			if cmd.Name == name {
				return cmd.Content, nil
			}
		}
	}
	return "", os.ErrNotExist
}

// ListCommands returns all commands from all discovered plugins.
func (m *Manager) ListCommands() []Command {
	plugins, err := m.Discover()
	if err != nil {
		return nil
	}
	var all []Command
	for _, plugin := range plugins {
		all = append(all, LoadCommands(plugin.Path)...)
	}
	return all
}

func extractFirstLine(content string) string {
	lines := strings.SplitN(content, "\n", 2)
	if len(lines) == 0 {
		return ""
	}
	line := strings.TrimSpace(lines[0])
	line = strings.TrimPrefix(line, "#")
	return strings.TrimSpace(line)
}
