package agent

import "sort"

// Mode defines an agent operating mode with its own policy and prompt.
type Mode struct {
	Name        string
	Description string
	Policy      SprintPolicy
	Prompt      string
}

// DefaultModes returns the agent operating modes.
//
// Historically there were three modes (plan/build/explore). BUILD was removed:
// execution is now delegated from PLAN to the "builder" subagent via
// execute_task. The planner (typically Gemma) orchestrates, the builder
// executes one task at a time with user approval on every mutation. Sessions
// persisted with mode="build" are silently re-mapped to "plan" in SetMode.
//
// commit, debug, docs, reviewer, tester, refactorer, summarizer are subagents,
// not modes.
func DefaultModes() map[string]Mode {
	return map[string]Mode{
		"plan": {
			Name:        "plan",
			Description: "Planner. Designs the work, writes the plan, and dispatches builder subagents per task.",
			Policy:      NewPlanPolicy(),
			Prompt: "You are in plan mode. You are the orchestrator: design the work, write the plan, then DELEGATE each task to the builder subagent via execute_task. You never edit files directly.\n" +
				"STEP 1: If the user's request leaves scope, constraints, tech choices, or success criteria ambiguous, call ask_user (3-6 focused questions, one per call) BEFORE anything else. Wait for the answers. Only skip this step when the user's request is already fully specified OR a prior plan already answers these questions.\n" +
				"When calling ask_user, ALWAYS include an `options` array with exactly 3 short, mutually-exclusive suggested answers the user can pick with arrow keys. Example: {\"question\":\"Which CSS framework?\",\"options\":[\"Vanilla CSS\",\"Tailwind\",\"Bootstrap\"]}. The TUI adds a 'Write my own' row automatically, so do not include one.\n" +
				"STEP 2: Call plan_write with the full plan document — summary, context, assumptions, approach, possible stubs, risks, and validation.\n" +
				"STEP 3: Call todo_write with a fresh executable checklist (or task_* tools for incremental changes). The checklist is not the full plan.\n" +
				"STEP 4: For each task in the checklist, call execute_task with {\"task_id\":\"plan-N\",\"relevant_files\":[\"path1\",\"path2\"]}. relevant_files is the MINIMAL list of paths the builder needs — do NOT pass the plan document, the full checklist, or wide globs. The builder runs under its own model (editor role) with user approval for every edit. After the builder returns, read the result, mark the task with task_update(status=\"completed\") if successful, or ajust and retry. Then proceed to the next task. Do not ask the user for confirmation between tasks — the approval system fires per edit.\n" +
				"If a prior plan or tasks exist, read them first with plan_get / task_list and preserve what still applies.\n" +
				"After steps 2 and 3 are both done in the same turn, give a one-sentence summary before you start dispatching execute_task.",
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
