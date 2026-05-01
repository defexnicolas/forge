package skills

import (
	"os"
	"path/filepath"
	"strings"
)

// SkillMeta holds parsed frontmatter from a SKILL.md file.
type SkillMeta struct {
	Name        string
	Description string
	Tools       []string
	Models      []string
	Content     string // body after frontmatter
}

// SkillDetail combines metadata with file path.
type SkillDetail struct {
	Meta SkillMeta
	Path string
}

// LoadSkill reads a SKILL.md from a skills directory and parses its frontmatter.
func (m *Manager) LoadSkill(name string) (*SkillDetail, error) {
	for _, root := range m.searchDirs() {
		skillFile := filepath.Join(root, name, "SKILL.md")
		data, err := os.ReadFile(skillFile)
		if err != nil {
			continue
		}
		meta := parseFrontmatter(string(data))
		if meta.Name == "" {
			meta.Name = name
		}
		return &SkillDetail{Meta: meta, Path: skillFile}, nil
	}
	return nil, &os.PathError{Op: "load skill", Path: name, Err: os.ErrNotExist}
}

func parseFrontmatter(content string) SkillMeta {
	meta := SkillMeta{}
	if !strings.HasPrefix(content, "---\n") {
		meta.Content = content
		return meta
	}
	end := strings.Index(content[4:], "\n---")
	if end < 0 {
		meta.Content = content
		return meta
	}
	front := content[4 : 4+end]
	meta.Content = strings.TrimSpace(content[4+end+4:])

	for _, line := range strings.Split(front, "\n") {
		line = strings.TrimSpace(line)
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		switch key {
		case "name":
			meta.Name = val
		case "description":
			meta.Description = val
		case "tools":
			meta.Tools = parseList(val)
		case "models":
			meta.Models = parseList(val)
		}
	}
	return meta
}

func parseList(val string) []string {
	// Handle YAML-like [a, b, c] or comma-separated.
	val = strings.Trim(val, "[]")
	parts := strings.Split(val, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
