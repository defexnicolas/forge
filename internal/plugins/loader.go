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

// AgentDef represents a subagent definition from a plugin. Frontmatter
// fields (parsed from a leading `---` block) populate Tools and ModelRole;
// the body that follows the frontmatter ends up in Body and is what we
// inject as the agent's system prompt.
type AgentDef struct {
	Name        string
	Content     string // raw file contents, frontmatter included
	Body        string // post-frontmatter body — system-prompt material
	Source      string
	Description string
	Tools       []string
	ModelRole   string
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
// Frontmatter fields recognised: name (overrides filename), description,
// tools (YAML-list or comma-separated), model_role. Anything else is
// ignored — keeping the schema minimal makes future fields opt-in instead
// of breaking existing plugins.
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
		fileName := strings.TrimSuffix(entry.Name(), ".md")
		content := string(data)
		fm, body := parseAgentFrontmatter(content)
		name := fm["name"]
		if name == "" {
			name = fileName
		}
		desc := fm["description"]
		if desc == "" {
			desc = extractFirstLine(body)
		}
		agents = append(agents, AgentDef{
			Name:        name,
			Content:     content,
			Body:        body,
			Source:      pluginName,
			Description: desc,
			Tools:       parseListField(fm["tools"]),
			ModelRole:   fm["model_role"],
		})
	}
	return agents
}

// parseAgentFrontmatter reads a leading `---\n...\n---\n` YAML-ish block
// and returns the key/value map plus everything after. Missing or
// malformed frontmatter yields an empty map and the original content as
// body — degrade gracefully so a Claude Code agent file with no
// frontmatter still loads (just with default tools/model_role).
func parseAgentFrontmatter(content string) (map[string]string, string) {
	out := map[string]string{}
	if !strings.HasPrefix(content, "---") {
		return out, content
	}
	rest := strings.TrimPrefix(content, "---")
	rest = strings.TrimLeft(rest, "\r\n")
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return out, content
	}
	header := rest[:end]
	body := strings.TrimLeft(rest[end+4:], "\r\n")
	for _, line := range strings.Split(header, "\n") {
		line = strings.TrimRight(line, "\r")
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		k := strings.TrimSpace(line[:idx])
		v := strings.TrimSpace(line[idx+1:])
		if k == "" {
			continue
		}
		out[k] = v
	}
	return out, body
}

// parseListField turns either YAML-style "[a, b, c]" or a comma-separated
// "a, b, c" into []string. Empty input -> nil.
func parseListField(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if strings.HasPrefix(raw, "[") && strings.HasSuffix(raw, "]") {
		raw = strings.TrimPrefix(raw, "[")
		raw = strings.TrimSuffix(raw, "]")
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		v := strings.Trim(strings.TrimSpace(part), `"'`)
		if v != "" {
			out = append(out, v)
		}
	}
	return out
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
