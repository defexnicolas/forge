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
			Prompt: "You are in build mode. You can read code, make edits (with approval), and run commands.\n" +
				"Be efficient: prefer a small number of decisive tool calls over long exploration. " +
				"Before each read or search, ask yourself if you already know the answer from prior context — skip the call if so. " +
				"Call apply_patch / write_file / edit_file as soon as you have what you need; do not restate the plan in prose before acting. " +
				"Batch related tiny changes into one apply_patch rather than multiple edit_file calls. " +
				"After a verification command passes, stop — do not re-inspect files to narrate the result.",
		},
		"plan": {
			Name:        "plan",
			Description: "Analysis mode. Proposes changes without editing files.",
			Policy:      NewPlanPolicy(),
			Prompt: "You are in plan mode. Your job is to create or refine a full plan document, then keep a separate executable checklist.\n" +
				"Do NOT edit, write, or create files.\n" +
				"STEP 1: If the user's request leaves scope, constraints, tech choices, or success criteria ambiguous, call ask_user (3-6 focused questions, one per call) BEFORE anything else. Wait for the answers. Only skip this step when the user's request is already fully specified OR a prior plan already answers these questions.\n" +
			"When calling ask_user, ALWAYS include an `options` array with exactly 3 short, mutually-exclusive suggested answers the user can pick with arrow keys. Example: {\"question\":\"Which CSS framework?\",\"options\":[\"Vanilla CSS\",\"Tailwind\",\"Bootstrap\"]}. The TUI adds a 'Write my own' row automatically, so do not include one.\n" +
				"STEP 2: Call plan_write with the full plan document — summary, context, assumptions, approach, possible stubs, risks, and validation.\n" +
				"STEP 3: Call todo_write with a fresh executable checklist (or task_* tools for incremental changes). The checklist is not the full plan.\n" +
				"If a prior plan or tasks exist, read them first with plan_get/task_list and preserve what still applies.\n" +
				"After steps 2 and 3 are both done in the same turn, give a one-sentence summary.",
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
