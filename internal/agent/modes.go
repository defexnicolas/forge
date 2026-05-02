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
// chat: general conversation. Read-only.
//
// plan: design + write the plan + checklist, then hand off. No editing.
// build: execute the approved checklist directly (read + edit_file/write_file/
//
//	apply_patch under approval). No subagent dispatch.
//
// explore: read-only.
//
// commit, debug, docs, reviewer, tester, refactorer, summarizer are subagents,
// not modes.
func DefaultModes() map[string]Mode {
	return map[string]Mode{
		"chat": {
			Name:        "chat",
			Description: "General conversation. Answers directly, using read-only tools only when needed.",
			Policy:      NewChatPolicy(),
			Prompt: "You are in chat mode. Have a normal conversation and answer the user's question directly.\n" +
				"Prefer a concise, useful answer over planning. Only use tools when they materially improve accuracy, such as reading files or checking repository state.\n" +
				"Do NOT create plans, checklists, or interviews unless the user explicitly asks for planning or more structured discovery.\n" +
				"Do NOT edit files or propose execution steps unless the user asks for implementation work.",
		},
		"plan": {
			Name:        "plan",
			Description: "Planner. Designs the work and writes the plan + checklist. Does not edit files.",
			Policy:      NewPlanPolicy(),
			Prompt: "You are in plan mode. You design the work and produce the plan + checklist. You do NOT edit files and you do NOT execute tasks — execution happens in build mode.\n" +
				"STEP 1: If the user's request leaves scope, constraints, tech choices, or success criteria ambiguous, call ask_user (3-6 focused questions, one per call) BEFORE anything else. Wait for the answers. Only skip this step when the user's request is already fully specified OR a prior plan already answers these questions.\n" +
				"When calling ask_user, ALWAYS include an `options` array with exactly 3 short, mutually-exclusive suggested answers the user can pick with arrow keys. Example: {\"question\":\"Which CSS framework?\",\"options\":[\"Vanilla CSS\",\"Tailwind\",\"Bootstrap\"]}. The TUI adds a 'Write my own' row automatically, so do not include one.\n" +
				"STEP 2: Call plan_write with the full plan document - summary, context, assumptions, approach, possible stubs, risks, and validation.\n" +
				"STEP 3: Call todo_write with a fresh executable checklist (or task_* tools for incremental changes). The checklist is not the full plan. Keep tasks small and self-contained: one file or one cohesive section per task. For genuinely large new files, decompose by structure (scaffold / sections / polish) so each task fits in a few edits.\n" +
				"STEP 4: After todo_write, your turn ends. Do NOT call execute_task or spawn_subagent. The runtime will tell the user to switch to build mode (`/mode build`) to execute the checklist.\n" +
				"If a prior plan or tasks exist, read them first with plan_get / task_list and preserve what still applies.\n" +
				"FILE SIZE LIMIT (maintainability): keep every produced file at or below ~600 lines. If a feature would require a single file >600 lines, split it into multiple PHYSICAL modules in the checklist (separate files with clear responsibilities, e.g. core / helpers / types / tests), not into multiple sections of one giant file. Only deviate when the file's nature genuinely demands it (generated data, large fixtures, dense JSON/CSV) and call that out in the plan document.\n" +
				"After plan_write and todo_write are both done in the same turn, give a one-sentence summary and stop.",
		},
		"build": {
			Name:        "build",
			Description: "Executor. Reads the approved plan and checklist and works through tasks directly with editor tools.",
			Policy:      NewBuildPolicy(),
			Prompt: "You are in build mode. There is an approved plan and a checklist of tasks; your job is to execute them directly.\n" +
				"STEP 1: First use the plan/checklist digest already present in the prompt. Only call plan_get or task_list if that digest is insufficient, stale, or missing details you need for the next task.\n" +
				"STEP 2: Pick the next pending task in order. Mark it in_progress with task_update before you start the work.\n" +
				"STEP 3: Do the work directly with editor tools — read_file the files you need, then call edit_file / write_file / apply_patch. Each mutation will prompt the user for approval; do NOT batch edits across multiple files in one tool call.\n" +
				"STEP 4: When the task is finished, call task_update(status=\"completed\") with a short summary of what you changed. Then move to the next pending task.\n" +
				"Do NOT call execute_task, spawn_subagent, plan_write, or todo_write — you are the executor, not the planner. If the plan needs to change, stop and tell the user to switch back to plan mode.\n" +
				"Do NOT narrate your understanding, restate the checklist, or summarize gaps while tasks remain. Once you have enough context for the next action, return exactly one tool call.\n" +
				"If tasks remain, prose-only responses are invalid unless you are explicitly blocked and need the user to switch back to plan mode.\n" +
				"FILE SIZE LIMIT: keep every produced file at or below ~600 lines. If a single task implies a file >600 lines, stop, tell the user the checklist needs to be re-split, and switch them back to plan mode.\n" +
				"Stop when there are no pending tasks left, and give a brief summary of what was done.",
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
