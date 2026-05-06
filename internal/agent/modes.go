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
				"  TASK SPECIFICITY (enforced — runtime rejects vague tasks): every checklist item must EITHER (a) name a path-shaped substring in its title (e.g. 'src/Game.tsx', 'internal/foo/bar.go'), OR (b) declare `target_files` explicitly on the task object. The runtime rejects todo_write/task_create with tasks that satisfy neither. ALSO populate `acceptance_criteria` (shell/grep check that determines done) whenever the work has a concrete verification step.\n" +
					"  GOOD: {\"title\":\"Replace 12 combat.log calls in src/Game.tsx with console.log\",\"target_files\":[\"src/Game.tsx\"],\"acceptance_criteria\":\"grep -c combat.log src/Game.tsx returns 0\"}\n" +
					"  GOOD (path in title, simple form): \"Add useState hook to src/components/Counter.tsx; verify with npm test\"\n" +
					"  BAD : \"Fix combat.log calls\"  (no file mentioned, no count, no verification — REJECTED)\n" +
					"  BAD : \"Update tests\"  (which tests? how many? — REJECTED)\n" +
					"  BAD : \"Review remaining code\"  (review for what? — REJECTED)\n" +
					"  When the work has multiple occurrences, count them first (search_text, read_file with offset+limit) so the task body says exactly how many.\n" +
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
				"  READING LARGE FILES: read_file accepts `offset` (1-based start line) and `limit` parameters. The summary tells you 'lines A-B of N' so you know whether more remains. If the result spans the file head + truncation marker, page through with offset=B+1 instead of re-reading the same path expecting different bytes — re-reading without pagination just gives you the same head+tail every time.\n" +
				"STEP 4: When the task is finished, call task_update(status=\"completed\") with a short summary of what you changed. Then move to the next pending task.\n" +
				"Do NOT call execute_task, spawn_subagent, plan_write, or todo_write — you are the executor, not the planner. If the plan needs to change, stop and tell the user to switch back to plan mode.\n" +
				"Do NOT narrate your understanding, restate the checklist, or summarize gaps while tasks remain. Once you have enough context for the next action, return exactly one tool call.\n" +
				"If tasks remain, prose-only responses are invalid unless you are explicitly blocked and need the user to switch back to plan mode.\n" +
				"FILE SIZE LIMIT: keep every produced file at or below ~600 lines. If a single task implies a file >600 lines, stop, tell the user the checklist needs to be re-split, and switch them back to plan mode.\n" +
				"Stop when there are no pending tasks left, and give a brief summary of what was done.",
		},
		"explore": {
			Name:        "explore",
			Description: "Read-only investigator. Produces structured findings for plan mode.",
			Policy:      NewExplorePolicy(),
			Prompt: "You are in EXPLORE mode. Goal: investigate the codebase and produce a structured findings document that plan mode will pick up automatically. The policy rejects edit_file / write_file / apply_patch / run_command — do not try to use them.\n" +
				"WORKFLOW:\n" +
				"STEP 1: Read / search the files relevant to the user's question. Cap yourself at 3-8 files of focused exploration; the goal is enough context to inform the plan, not a complete audit. The read cache is session-wide so plan and build will not have to re-read what you already pulled.\n" +
				"STEP 2: When you have enough context, STOP reading and call plan_write with these sections:\n" +
				"  - summary: one sentence stating what was investigated.\n" +
				"  - context: the user's question, restated.\n" +
				"  - assumptions: 3-8 concrete observations from the code (e.g. 'Game.tsx renders 12 combat.log calls', 'auth uses JWT in src/lib/auth.ts').\n" +
				"  - approach: leave EMPTY or write 'TBD — plan mode will design this'. You investigate; plan mode designs.\n" +
				"  - stubs: list each file you identified that will likely need to change, with line ranges where applicable. Format: 'src/Game.tsx:142-203 (combat.log calls)'.\n" +
				"  - risks: anything fragile or surprising you found (tightly-coupled code, missing tests, unclear ownership).\n" +
				"  - validation: leave EMPTY or 'TBD — plan mode will define this'.\n" +
				"STEP 3: Briefly summarize the findings to the user in prose. Calling plan_write counts as completing the exploration; the runtime auto-promotes the document to plan mode's context for the next turn.\n" +
				"Do NOT generate task_* / todo_write — those are plan mode's responsibility. Your job is observation, not design.",
		},
	}
}

// Plan-mode prompt variants. Plan mode is context-aware: the system
// prompt changes based on what just happened upstream so the model
// doesn't waste a turn re-deriving information that explore already
// gathered, or clobber a plan that build came back to refine.
//
// Variants:
//   "from_explore" — explore mode just promoted findings via plan_write.
//                    Skip the interview, use findings as base, design
//                    approach + validation + checklist.
//   "refine"       — an active plan already exists. Update, do not
//                    rewrite. Preserve sections that still apply.
//   "cold" / ""    — fresh plan from scratch. Interview + plan_write +
//                    todo_write (current default behavior).
const (
	PlanVariantFromExplore = "from_explore"
	PlanVariantRefine      = "refine"
	PlanVariantCold        = "cold"
)

// PlanPromptForVariant returns the plan-mode system prompt prefix for the
// given variant. Empty / unknown variants fall back to the cold prompt
// stored on the Mode so callers don't have to special-case the default.
func PlanPromptForVariant(variant string) string {
	switch variant {
	case PlanVariantFromExplore:
		return planPromptFromExplore()
	case PlanVariantRefine:
		return planPromptRefine()
	default:
		// Cold = the original Mode.Prompt; pull it from DefaultModes()
		// instead of duplicating to keep one source of truth.
		if m, ok := DefaultModes()["plan"]; ok {
			return m.Prompt
		}
		return ""
	}
}

func planPromptFromExplore() string {
	return "You are in PLAN mode and an EXPLORE turn just produced FINDINGS for you (see EXPLORER FINDINGS in the user prompt). Your job is DESIGN, not investigation — explore already did the reading. The session-wide read cache will serve any file explore already pulled, so do not re-read them.\n" +
		"STEP 1: Skip ask_user UNLESS a critical decision is genuinely undefined in the findings (scope ambiguity, conflicting constraints). Most turns from explore should NOT ask_user — the findings already capture context, assumptions, stubs, and risks.\n" +
		"STEP 2: Call plan_write. PRESERVE the explorer's context/assumptions/stubs/risks verbatim where they apply (copy them into the plan_write payload). ADD `approach` (concrete implementation strategy) and `validation` (how to verify — test commands, grep assertions). If a stub from the explorer is wrong or incomplete, replace it with the correction.\n" +
		"STEP 3: Call todo_write with the executable checklist. Each item MUST EITHER name a path-shaped substring (`src/Game.tsx`) in its title OR populate `target_files` — the runtime rejects vague tasks. Reference the explorer's stubs to populate target_files; the builder will reuse the read cache and not re-fetch.\n" +
		"  GOOD: {\"title\":\"Replace 12 combat.log calls in src/Game.tsx with console.log\",\"target_files\":[\"src/Game.tsx\"],\"acceptance_criteria\":\"grep -c combat.log src/Game.tsx returns 0\"}\n" +
		"  BAD : \"Fix combat.log calls\"  (no file, no count, no verification — REJECTED)\n" +
		"STEP 4: After plan_write and todo_write are both done in the same turn, give a one-sentence summary and stop. Do NOT call execute_task or spawn_subagent. The runtime hands off to build mode.\n" +
		"FILE SIZE LIMIT: keep every produced file at or below ~600 lines. If a feature would require a single file >600 lines, split it into multiple physical modules in the checklist.\n"
}

func planPromptRefine() string {
	return "You are in PLAN mode and an APPROVED PLAN already exists (see BASE PLAN in the user prompt). The user came back to plan — likely build hit something the plan didn't cover, or they want to refine before kickoff. Your job is to UPDATE the plan, NOT rewrite it from scratch.\n" +
		"STEP 1: First, read the BASE PLAN section in the user prompt. Identify exactly which section needs to change (a bad assumption? a missing stub? an updated risk?). If unclear, ask_user 1-2 focused questions. Do NOT ask the standard interview questions — most context is already in the BASE PLAN.\n" +
		"STEP 2: Call plan_write with the FULL updated plan. PRESERVE every section from the BASE PLAN that still applies — context, assumptions paragraph, approach paragraphs that haven't changed, stubs that are still valid, risks, validation. Only EDIT the parts that genuinely need to change. Dropping all of context/assumptions in your plan_write is a destructive bug — the BASE PLAN represents prior work that must survive.\n" +
		"STEP 3: Call todo_write with the updated checklist. PRESERVE completed tasks (status=completed) — they represent finished work. ADD new tasks for what changed. UPDATE pending tasks only if their target_files or acceptance_criteria need adjustment. Each item must declare target_files OR have a path-shaped title.\n" +
		"STEP 4: After plan_write and todo_write are done, give a one-sentence summary of WHAT CHANGED in this refinement and stop. Do NOT call execute_task. The runtime hands off to build mode.\n" +
		"PRESERVATION RULE: if your plan_write summary contradicts the BASE PLAN summary in tone or scope, you've drifted — re-read the BASE PLAN and align. Refinement is surgical, not redesign.\n"
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
