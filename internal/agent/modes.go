package agent

import "sort"

// Mode defines an agent operating mode with its own policy and prompt.
type Mode struct {
	Name        string
	Description string
	Policy      SprintPolicy
	Prompt      string
}

// DefaultModes returns the 3 principal agent modes.
// commit, debug, and review are subagents, not modes.
func DefaultModes() map[string]Mode {
	return map[string]Mode{
		"build": {
			Name:        "build",
			Description: "Full coding agent. Can read, edit (with approval), and run commands.",
			Policy:      NewSprintPolicy(),
			Prompt:      "You are in build mode. You can read code, make edits (with approval), and run commands.",
		},
		"plan": {
			Name:        "plan",
			Description: "Analysis mode. Proposes changes without editing files.",
			Policy:      NewPlanPolicy(),
			Prompt:      "You are in plan mode. Your ONLY job is to create a plan using the todo_write tool. Do NOT write code, do NOT include code blocks, do NOT explain implementation details. FIRST call todo_write with a list of steps, THEN give a one-sentence summary. Nothing else.",
		},
		"explore": {
			Name:        "explore",
			Description: "Read-only mode for understanding code.",
			Policy:      NewExplorePolicy(),
			Prompt:      "You are in explore mode. Read and understand the codebase. Do not make any changes.",
		},
	}
}

// GetMode returns a mode by name.
func GetMode(name string) (Mode, bool) {
	m, ok := DefaultModes()[name]
	return m, ok
}

// ModeNames returns sorted mode names.
func ModeNames() []string {
	modes := DefaultModes()
	names := make([]string, 0, len(modes))
	for name := range modes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
