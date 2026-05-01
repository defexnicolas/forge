package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// builtinSkill defines a skill that ships with forge.
type builtinSkill struct {
	Name        string
	Description string
	Content     string // SKILL.md body
}

// BuiltinSkills returns the catalog of skills available for installation.
func BuiltinSkills() []builtinSkill {
	return []builtinSkill{
		{
			Name:        "commit",
			Description: "Create well-formatted git commits",
			Content: `---
name: commit
description: Create well-formatted git commits
tools: [run_command, read_file, search_text]
---
When asked to commit, follow these steps:
1. Run "git status" to see changed files
2. Run "git diff --staged" to review staged changes (or "git diff" for unstaged)
3. Analyze the changes and draft a concise commit message following conventional commits format
4. Stage relevant files with "git add" (prefer specific files over "git add .")
5. Create the commit with "git commit -m <message>"
`,
		},
		{
			Name:        "review",
			Description: "Review code changes and provide feedback",
			Content: `---
name: review
description: Review code changes and provide feedback
tools: [read_file, search_text, search_files, git_diff]
---
When asked to review code:
1. Run "git diff" or "git diff HEAD~1" to see recent changes
2. Read the changed files for full context
3. Check for: bugs, security issues, performance problems, style inconsistencies
4. Provide clear, actionable feedback organized by severity (critical, suggestion, nit)
`,
		},
		{
			Name:        "test",
			Description: "Generate and run tests for code",
			Content: `---
name: test
description: Generate and run tests for code
tools: [read_file, write_file, run_command, search_text]
---
When asked to write or run tests:
1. Read the target file to understand what needs testing
2. Identify the testing framework used in the project
3. Write tests covering: happy path, edge cases, error handling
4. Run the tests and fix any failures
`,
		},
		{
			Name:        "refactor",
			Description: "Refactor code for clarity and maintainability",
			Content: `---
name: refactor
description: Refactor code for clarity and maintainability
tools: [read_file, edit_file, search_text, run_command]
---
When asked to refactor:
1. Read the target code and understand its purpose
2. Identify issues: duplication, long functions, unclear naming, tight coupling
3. Make small, incremental changes with clear purpose
4. Run tests after each change to ensure nothing breaks
`,
		},
		{
			Name:        "explain",
			Description: "Explain code structure and logic",
			Content: `---
name: explain
description: Explain code structure and logic
tools: [read_file, search_text, search_files, list_files]
---
When asked to explain code:
1. Read the target files
2. Identify the architecture: entry points, data flow, key abstractions
3. Explain at the right level of detail for the user's question
4. Use concrete references to file paths and line numbers
`,
		},
		{
			Name:        "debug",
			Description: "Debug issues and find root causes",
			Content: `---
name: debug
description: Debug issues and find root causes
tools: [read_file, search_text, run_command, search_files]
---
When asked to debug:
1. Understand the symptom: what's expected vs what's happening
2. Search for relevant error messages, function names, or variables
3. Read the code path that could produce the issue
4. Form a hypothesis and verify it by reading more code or running commands
5. Propose a fix with explanation of root cause
`,
		},
	}
}

// SearchAvailable returns built-in skills filtered by query, excluding already installed ones.
func SearchAvailable(query string, installed map[string]bool) []Skill {
	query = strings.ToLower(query)
	var skills []Skill
	for _, b := range BuiltinSkills() {
		if installed[b.Name] {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(b.Name), query) && !strings.Contains(strings.ToLower(b.Description), query) {
			continue
		}
		skills = append(skills, Skill{
			Name:        b.Name,
			Description: b.Description,
			Installed:   false,
			Source:      "builtin",
		})
	}
	return skills
}

// InstallBuiltin writes a built-in skill's SKILL.md to the local skills directory.
func InstallBuiltin(cwd, name string) error {
	for _, b := range BuiltinSkills() {
		if b.Name == name {
			dir := filepath.Join(cwd, ".forge", "skills", name)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(b.Content), 0o644)
		}
	}
	return fmt.Errorf("skill not found: %s", name)
}

// ScanLocal returns skills found in .forge/skills/ directories and any
// plugin-shipped skills/ subdirectories registered via Options.PluginSkillDirs.
func (m *Manager) ScanLocal() []Skill {
	var skills []Skill
	seen := map[string]bool{}
	for _, dir := range m.searchDirs() {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if seen[entry.Name()] {
				continue
			}
			skillDir := filepath.Join(dir, entry.Name())
			stat, err := os.Stat(skillDir)
			if err != nil || !stat.IsDir() {
				continue
			}
			skillFile := filepath.Join(skillDir, "SKILL.md")
			if _, err := os.Stat(skillFile); err != nil {
				continue
			}
			description := "installed locally"
			if data, err := os.ReadFile(skillFile); err == nil {
				if meta := parseFrontmatter(string(data)); meta.Description != "" {
					description = meta.Description
				}
			}
			seen[entry.Name()] = true
			skills = append(skills, Skill{
				Name:        entry.Name(),
				Description: description,
				Installed:   true,
				InstallPath: skillDir,
				Source:      installedSource(dir),
			})
		}
	}
	return skills
}

func (m *Manager) FindInstalled(name string) (Skill, bool) {
	for _, skill := range m.ScanLocal() {
		if skill.Name == name {
			return skill, true
		}
	}
	return Skill{}, false
}

func (m *Manager) RemoveInstalled(name string) (Skill, error) {
	skill, ok := m.FindInstalled(name)
	if !ok {
		return Skill{}, fmt.Errorf("installed skill not found: %s", name)
	}
	if skill.Source == "plugin" {
		return Skill{}, fmt.Errorf("plugin-shipped skill is read-only; disable the plugin instead")
	}
	if skill.Source != "project" && skill.Source != "legacy" {
		return Skill{}, fmt.Errorf("global install is read-only from Forge")
	}
	allowedRoots := []string{
		filepath.Join(m.cwd, ".agents", "skills"),
		filepath.Join(m.cwd, ".forge", "skills"),
	}
	if err := validateRemovePath(skill.InstallPath, allowedRoots); err != nil {
		return Skill{}, err
	}
	if err := os.RemoveAll(skill.InstallPath); err != nil {
		return Skill{}, err
	}
	return skill, nil
}

func validateRemovePath(target string, allowedRoots []string) error {
	if target == "" {
		return fmt.Errorf("install path is empty")
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	if info, err := os.Lstat(targetAbs); err != nil {
		return err
	} else if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to remove symlinked skill path: %s", target)
	}
	for _, root := range allowedRoots {
		rootAbs, err := filepath.Abs(root)
		if err != nil {
			return err
		}
		if isPathWithin(targetAbs, rootAbs) {
			return nil
		}
	}
	return fmt.Errorf("refusing to remove skill outside project skills dirs: %s", target)
}

func isPathWithin(path, root string) bool {
	cleanPath := filepath.Clean(path)
	cleanRoot := filepath.Clean(root)
	if cleanPath == cleanRoot {
		return false
	}
	rel, err := filepath.Rel(cleanRoot, cleanPath)
	if err != nil {
		return false
	}
	return rel != "." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && rel != ".."
}

func installedSource(dir string) string {
	clean := filepath.ToSlash(dir)
	switch {
	case strings.Contains(clean, "/plugins/"):
		return "plugin"
	case strings.Contains(clean, ".agents/skills"):
		return "project"
	case strings.Contains(clean, ".codex/skills"):
		return "global"
	default:
		return "legacy"
	}
}
