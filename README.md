# Forge

**A terminal coding agent built for local models.** Lean context, live parallel subagents, native plugin/skill/MCP ecosystem compatibility, and a TUI that streams at 30fps without dropping frames on Ollama's 150+ tk/s bursts.

Written in Go. Runs against LM Studio, Ollama (via any OpenAI-compatible endpoint), and the OpenAI API. Plugs into the Claude Code plugin format, consumes skills from [skills.sh](https://skills.sh), and speaks MCP.

---

## Why Forge

| | **Forge** | OpenCode | Aider |
|---|---|---|---|
| **Per-turn context injection** | ~1k (YARN-scored) | ~10–16k (tool-descriptions + skills XML bloat) | 4–8k (repomap) |
| **Core language / binary** | Go, single 34MB `.exe` | TypeScript, Node runtime | Python, pip install |
| **TUI** | Bubble Tea, 30fps flush, inline diff, Glamour markdown | OpenTUI, 60fps, syntax highlighting broken ([#12301](https://github.com/sst/opencode/issues/12301)) | Rich terminal, REPL-style |
| **Parallel subagents** | Live multi-lane with `EventSubagentProgress` | Sequential despite appearing parallel ([#14195](https://github.com/sst/opencode/issues/14195)) | None |
| **Plan mode** | Two artifacts (plan doc + checklist), phase indicator, delegates to builder subagent | Tool-restricted agent swap | None (commit-loop style) |
| **Claude Code plugins** | Reads `.claude/plugins` + `.claude-plugin/plugin.json` natively | Partial | No |
| **MCP servers** | Full stdio + SSE transport from `.mcp.json` | Full | Partial |
| **Skills ecosystem** | `skills.sh` directory via `/skills` browser, install-scoped to project | No | No |
| **Local-first** | Designed against LM Studio; YARN profiles sized for 2B/4B/9B/14B/26B | Cloud-first, local possible | Cloud-first |
| **Approval UX** | Inline colored diff preview + keyboard confirm | Full-screen diff dialog | Per-edit y/n |
| **Remote viewer** | `/remote-control` serves a live SSE feed over LAN | No | No |

**The short version:** Forge injects an order of magnitude less context than OpenCode per turn because it uses YARN scoring + tool-result compaction instead of dumping the whole skills catalog + every tool's multi-paragraph description into every request. Your local model runs faster and answers sharper.

---

## Install

### Prerequisites

- **Go 1.25+** (`go version`)
- **LM Studio** (recommended) or any OpenAI-compatible endpoint
- Windows, macOS, or Linux

### Build from source

```bash
git clone <this-repo> forge
cd forge
go build -o forge.exe ./cmd/forge     # Windows
go build -o forge    ./cmd/forge       # macOS / Linux
```

Binary is ~34 MB, fully static, no runtime deps.

### Add to PATH

**Windows (PowerShell, current user):**
```powershell
$forgeDir = "C:\path\to\forge"
[Environment]::SetEnvironmentVariable("PATH", "$env:PATH;$forgeDir", "User")
# Restart terminal for the change to take effect.
```

**Windows (permanent, all users, admin PowerShell):**
```powershell
[Environment]::SetEnvironmentVariable("PATH", "$env:PATH;C:\path\to\forge", "Machine")
```

**macOS / Linux (zsh or bash):**
```bash
echo 'export PATH="$HOME/forge:$PATH"' >> ~/.zshrc   # or ~/.bashrc
source ~/.zshrc
# Or: sudo cp forge /usr/local/bin/
```

Verify:
```bash
forge --help
```

### First run

```bash
cd /your/project
forge
```

On first launch Forge creates `.forge/` in your project with default config, session log, and SQLite state. Nothing is written outside the project directory unless you explicitly pin a global skill or plugin.

---

## Quick Start

### With LM Studio (default, recommended for local)

1. Load a model in LM Studio with GEN slots ≥ 2.
2. Start the LM Studio server on `http://localhost:1234/v1`.
3. Run `forge` in your project root.
4. Forge auto-detects the loaded model via the `/v1/models` endpoint.

### With OpenAI API

Edit `.forge/config.toml`:
```toml
[providers.default]
name = "openai_compatible"

[providers.openai_compatible]
base_url = "https://api.openai.com/v1"
api_key_env = "OPENAI_API_KEY"
default_model = "gpt-5.4-mini"
supports_tools = true
```

Export the key and run:
```bash
export OPENAI_API_KEY=sk-...
forge
```

Any OpenAI-compatible endpoint works: vLLM, llama.cpp's server, Groq, Together, Fireworks. Just point `base_url` at it.

---

## Core Concepts

### Modes

Two modes cycle with **Shift+Tab**.

| Mode | Role | Tools allowed |
|---|---|---|
| **EXPLORE** | Read-only investigation. The model reads, searches, and reports — no edits possible. | `read_file`, `list_files`, `search_text`, `search_files`, `git_status`, `git_diff`, `web_fetch` |
| **PLAN** | Orchestrator. Interviews the user, writes a plan document, populates a checklist, then delegates each task to the `builder` subagent via `execute_task`. The builder is the one that actually mutates files (with approval). | `ask_user`, `plan_write`, `todo_write`, `execute_task`, `task_*`, plus read-only tools |

**There is no separate BUILD mode.** PLAN is the orchestrator; the `builder` subagent is what executes a single task end-to-end (read files → edit with approval → verify). This split is deliberate: the planner keeps a stable context across the whole workstream while each builder turn gets a fresh, tightly-scoped context for its single task.

### The recommended workflow

```
  EXPLORE      PLAN → builder subagent
  ────────     ─────────────────────────────
  1. ask questions    3. Ctrl+Shift+Tab → PLAN
  2. read files       4. answer interview questions
                      5. approve plan
                      6. builder works one task at a time
```

**Always start in EXPLORE.** The first few minutes of any non-trivial task should be the model reading your code — *not* writing any. In EXPLORE mode there are no approval modals, no undo stack to worry about, and the model's output is pure signal about what it understands.

Once you're confident the model has the right mental model of your code, switch to PLAN with `Shift+Tab`. The planner will ask 3–6 clarifying questions, write a plan document (`plan_write`), produce a checklist (`todo_write`), and offer to execute it. You can review the checklist in the right-hand panel (which shows `EXPLORE → DESIGN → REVIEW → EXECUTE` as the active phase) before approving.

### Subagents

Forge ships with 9 built-in subagents. Invoke them manually with `/agent <name> <task>` or let the planner dispatch them automatically.

| Subagent | Context | Tools | When |
|---|---|---|---|
| `explorer` | YARN-scored | read-only | investigation, preflight |
| `reviewer` | shared-read | read-only | diff review, PR sanity check |
| `tester` | forked | read + `run_command` | run allowlisted test commands |
| `builder` | forked | full mutating | executes ONE checklist task (dispatched by PLAN) |
| `refactorer` | forked | edit + write | scoped mechanical refactors |
| `docs` | shared-read | edit + write | update README, changelog |
| `commit` | shared-read | git + run_command | draft and stage conventional commits |
| `debug` | forked | read + run_command | root-cause a failing test or error |
| `summarizer` | YARN | read-only | compact session transcripts into YARN nodes |

Parallel dispatch lives in the `spawn_subagents` tool. The TUI renders each batch as a live multi-lane block — pending → running → completed/error, updated in place:

```
  parallel subagents (3)
    + [0] explorer    completed  found 4 call sites
    > [1] reviewer    running    reading internal/agent/runtime.go
    o [2] tester      pending
```

Parallel subagents cannot mutate files (enforced at dispatch); mutations go through `builder` sequentially.

### Native tools

All registered in `internal/tools/builtin.go`:

| Category | Tools |
|---|---|
| Filesystem | `read_file`, `list_files`, `write_file`, `edit_file`, `apply_patch` |
| Search | `search_text`, `search_files` |
| Git | `git_status`, `git_diff` |
| Shell | `run_command`, `powershell_command` (auto-selected on Windows) |
| Web | `web_fetch` (HTML → text via `golang.org/x/net/html`), `web_search` *(stub, pending)* |
| Plan/Task | `plan_write`, `plan_get`, `todo_write`, `task_create`, `task_list`, `task_get`, `task_update`, `execute_task` |
| Subagents | `spawn_subagent`, `spawn_subagents` (max_concurrency 1–8) |
| Interactive | `ask_user` (up to 3 suggested answers) |
| Skills | `skill` (invoke a registered skills.sh skill) |

Mutating tools (`write_file`, `edit_file`, `apply_patch`, `run_command`) route through the approval system — the TUI shows a colored unified diff with +/− counts before you confirm.

### Context engine: YARN

Forge's context moat is `internal/context/builder.go` + the YARN scoring engine:

- **YARN profiles** sized to the model: `2B` (5k budget), `4B` (6.5k), `9B` (8k, default), `14B` (12k), `26B` (20k).
- **Render modes**: `head` (default — summary + first N lines of content), `full`, `summary`.
- **Pins** and **@-mentions** are always-included — use `/pin @path/file.go` to lock a file into every turn.
- **Auto-compaction** — when the token budget is exceeded, the session is summarized into YARN nodes via the `summarizer` subagent.
- **Detected context length** — Forge queries LM Studio for the actual loaded context window and scales the budget proportionally (YaRN-extended Qwen, etc.).
- **Tool-result compaction** — only the last 3 tool results are kept verbatim; older ones are stubbed (`internal/agent/runtime.go: compactOldToolResults`).

Net effect: a typical Forge turn ships ~800–1,500 tokens of injection. OpenCode's equivalent sits at 10–16k because it dumps every tool description plus the XML skills catalog into every request.

---

## Multi-Model Loading

Even if you only have **one model loaded**, turn multi-model on. Here's why:

```toml
[model_loading]
enabled = true
strategy = "single"     # all roles reuse the active model
parallel_slots = 2      # <-- this is the one that matters
```

`parallel_slots` governs how many concurrent generation slots Forge requests from LM Studio when it loads a model. With `parallel_slots = 1`, every `spawn_subagents` call, every `/btw` question, and every subagent dispatch queues through a single slot — you'll see lanes sit in "running" state serially instead of truly in parallel.

With `parallel_slots = 2` (or more, VRAM permitting), three parallel subagents actually run concurrently and the multi-lane view evolves in real time instead of one-at-a-time.

**When to switch to `strategy = "parallel"`:** you have VRAM for multiple models loaded simultaneously *and* want per-role tuning — e.g. a larger 14B for `planner` and a fast 4B for `explorer`. Configure per-role via `/model-multi` or the `[models]` table:

```toml
[models]
chat      = "qwen3-14b"
explorer  = "qwen3-4b"
planner   = "qwen3-14b"
editor    = "qwen3-14b"
reviewer  = "qwen3-4b"
summarizer = "qwen3-4b"
```

---

## Claude Code Plugin Compatibility

Forge natively reads both `.forge/plugins/` and `.claude/plugins/` (project + user scope). If a directory contains a `.claude-plugin/plugin.json` manifest, Forge loads it with the same contract Claude Code uses — commands, hooks, skills, and agents all transfer over.

```toml
[plugins]
enabled = true
claude_compatible = true     # default
marketplaces = []            # extend with custom plugin marketplaces
```

List discovered plugins with `/plugins`. Hooks loaded from plugins appear in `/hooks` alongside project hooks.

---

## Skills (skills.sh)

Browse and install skills directly from within the TUI:

```
/skills                      # open browser, pick from the skills.sh directory
/skills refresh              # re-fetch catalog from https://skills.sh/
/skills vercel-labs/skills   # install from a specific repo
/skills cache                # inspect the local cache
```

Configured defaults (override in `.forge/config.toml`):

```toml
[skills]
cli = "npx"
directory_url = "https://skills.sh/"
repositories = ["vercel-labs/agent-skills", "vercel-labs/skills"]
agent = "codex"
install_scope = "project"    # skills live in .forge/skills/
copy = true                  # copy files locally for reproducibility
```

Installed skills are loaded into every turn's context as `kind:skill` YARN nodes and invokable via the `skill` tool.

---

## MCP (Model Context Protocol)

Drop an `.mcp.json` in your project root:

```json
{
  "mcpServers": {
    "github": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-github"],
      "env": {"GITHUB_PERSONAL_ACCESS_TOKEN": "ghp_..."}
    },
    "postgres": {
      "transport": "sse",
      "url": "http://localhost:3001/sse"
    }
  }
}
```

Both `stdio` (default) and `sse`/`http` transports work. List live MCP tools with `/mcp`.

---

## Hooks

Project-scoped hooks live in `.forge/hooks.json`:

```json
{
  "hooks": [
    {
      "event": "after:tool_call",
      "match": "edit_file",
      "command": "gofmt -w $FORGE_CHANGED_FILES",
      "timeout": 30
    },
    {
      "event": "session:end",
      "command": "git status"
    }
  ]
}
```

Events:
- `session:start`, `session:end`, `session:prompt`
- `before:tool_call` (blocking), `after:tool_call` (non-blocking)
- `before:compact`

Environment variables exposed: `FORGE_CWD`, `FORGE_TOOL`, `FORGE_EVENT`, `FORGE_CHANGED_FILES`.

---

## Remote Control

```
/remote-control start        # default port 9595
/remote-control status       # show LAN URL + viewer count
/remote-control stop
```

Serves the active session over LAN with a random bearer token:

- `GET /api/session` — JSON metadata
- `GET /api/stream` — SSE live feed of every agent event (mirrors TUI)
- `POST /api/input` — inject a prompt or slash command from a browser tab

Great for pairing over Tailscale, or running Forge on a beefy workstation while watching the stream from a laptop.

---

## Approval Profiles

Set with `/permissions set <profile>` or `approval_profile = "..."` in config.

| Profile | Shell commands | File edits |
|---|---|---|
| `safe` | all require approval | always ask |
| `normal` *(default)* | allowlist (git status/diff/log, npm/pnpm/yarn test, go test) | always ask |
| `fast` | broader allowlist (npm/pnpm/make/cargo run, pytest) | always ask |
| `yolo` | allow all except denylist (`rm -rf`, `git reset --hard`, `curl`, `wget`) | always ask |

File mutations (`edit_file`, `write_file`, `apply_patch`) always require approval regardless of profile — that's a hard invariant.

---

## Slash Commands Reference

| Command | Purpose |
|---|---|
| `/help` | full list with subcommands |
| `/mode [plan\|explore]` | switch mode (same as Shift+Tab) |
| `/model [list\|set\|reload]` | manage models and per-role assignments |
| `/model-multi [off]` | toggle multi-model routing |
| `/provider` | configure base URL / API key |
| `/plan [panel\|full\|todos\|new\|refine]` | manage plan document + checklist |
| `/plan-new <goal>` | clear plan, start fresh interview |
| `/agents` | list all 9 subagents |
| `/agent <name> <task>` | run a subagent once |
| `/btw <question>` | parallel side-channel question (doesn't block main agent) |
| `/tools` | list registered tools |
| `/mcp` | list MCP servers and their tools |
| `/plugins` | list discovered plugins |
| `/hooks` | list loaded hooks |
| `/skills [repo\|refresh\|cache]` | install / manage skills |
| `/context [pin\|drop\|yarn\|compact]` | manage context, pins, YARN |
| `/pin @path` / `/drop @path` | pin / unpin a file |
| `/yarn [settings\|profiles\|profile\|dry-run\|inspect]` | YARN tuning |
| `/compact` | compact session into YARN summary |
| `/diff` | show pending approval or workspace diff (colored) |
| `/approve` / `/reject` | handle pending approval |
| `/undo` | revert last approved edit |
| `/test [cmd]` | run an allowlisted test command |
| `/status` | mode, model, provider, permissions, context |
| `/config` | effective `.forge/config.toml` |
| `/session` | current session info |
| `/sessions` | recent sessions |
| `/resume <id\|latest>` | reopen a stored session |
| `/analyze [refresh\|show]` | scan + cache project snapshot |
| `/permissions [set <profile>]` | permission profile |
| `/theme <name>` | switch theme (see Custom Themes below) |
| `/think [on\|off]` | toggle thinking visibility |
| `/copy` | copy last response to clipboard |
| `/review` | reviewer subagent on current diff |
| `/log` | path to the live plain-text log |
| `/remote-control [start\|stop\|status]` | LAN viewer server |
| `/quit` | exit + save history |

---

## Keyboard Shortcuts

| Key | Action |
|---|---|
| `Shift+Tab` | cycle modes (EXPLORE ↔ PLAN) |
| `Ctrl+T` | toggle thinking visibility (live — re-renders the current stream) |
| `Ctrl+F` | history search |
| `Tab` | autocomplete `/command` or `@path` |
| `Enter` | submit |
| `Esc` → `Esc` | quit (double-tap confirms) |
| `Ctrl+C` | quit immediately |
| `PgUp` / `PgDn` | scroll viewport |

---

## Custom Themes

Built-ins: `default`, `light`, `ocean`, `mono`.

Drop your own as JSON in `.forge/themes/<name>.json`:

```json
{
  "name": "cosmic",
  "cyan":   "#6FD8E7",
  "green":  "#95F08D",
  "yellow": "#F0D474",
  "red":    "#F58E8E",
  "purple": "#C89BF0",
  "blue":   "#7EB7F5",
  "dim":    "#707070",
  "bright": "#FAFAFA",
  "bar_bg": "#1A1A1A",
  "input_bg": "#121212"
}
```

Colors accept `#rrggbb` hex or ANSI256 numbers (`"86"`). Apply with `/theme cosmic`.

---

## Benchmarks

**Qwen3 ~35B, Q4_K quantization, 8 GB VRAM + 32 GB DDR5 RAM:**

- Partial offload (MoE active experts on GPU) with `llama.cpp` / LM Studio
- Typical streaming: **~35-45 tk/s** sustained
- Forge TUI coalesces tokens at 33 ms intervals (~30 fps) — no frame drops, no janky redraws even under Ollama's burstiest output
- `tui.stream_flush_ms = 16` in config gives 60fps on hardware-accelerated terminals (iTerm2, WezTerm, Alacritty)

The TUI perf floor is an 8ms flush (clamped), so you can't accidentally freeze the event loop with a misconfigured value.

---

## Configuration

Full schema in `internal/config/config.go`. The most-touched keys:

```toml
# .forge/config.toml

default_agent = "plan"
approval_profile = "normal"          # safe | normal | fast | yolo

[providers.default]
name = "lmstudio"                     # or "openai_compatible"

[context]
engine = "yarn"                       # or "simple"
budget_tokens = 8000
auto_compact = true
model_context_tokens = 16384
reserve_output_tokens = 2000

[context.yarn]
profile = "9B"                        # 2B | 4B | 9B | 14B | 26B
render_mode = "head"                  # head | full | summary
render_head_lines = 40
pins = "always"
mentions = "always"

[model_loading]
enabled = true
strategy = "single"                   # or "parallel"
parallel_slots = 2                    # LM Studio GEN slots — bump for concurrency

[models]
chat = "qwen3-9b-q4_k_m"

[tui]
stream_flush_ms = 33                  # 16 for 60fps on modern terminals
```

---

## Architecture at a Glance

```
  ┌──────────────────────────────────────────────────────┐
  │ TUI — Bubble Tea, 30fps flush, fingerprint-keyed    │
  │   render cache, multi-lane subagent view             │
  └───────────────────────┬──────────────────────────────┘
                          │ event stream
  ┌───────────────────────┴──────────────────────────────┐
  │ Session Runtime — JSONL transcript, undo stack,     │
  │   approvals, event bus                               │
  └───────────────────────┬──────────────────────────────┘
                          │
  ┌───────────────────────┴──────────────────────────────┐
  │ Agent Runtime — modes, step loop, parser registry   │
  │ Context Builder — YARN scoring, skills, pins, mcps   │
  │ Tool Runtime — built-ins, external, MCP, plugins     │
  └───────────────────────┬──────────────────────────────┘
                          │
  ┌───────────────────────┴──────────────────────────────┐
  │ Policy Layer — approval profile, command denylist    │
  └───────────────────────┬──────────────────────────────┘
                          │
  ┌───────────────────────┴──────────────────────────────┐
  │ LLM Providers — OpenAI-compatible (SSE streaming)   │
  └──────────────────────────────────────────────────────┘
```

See `docs/ARCHITECTURE.md` for a deeper walkthrough (Spanish).

---

## Coming Soon

- **Patronus** — cloud model as advisor. A Patronus hook will route specific checkpoints (plan review, risky approval, critical refactor) through a cloud model (Claude Opus / GPT-5) as a second-opinion layer, while the main loop stays on your local model. Latency is amortized by making the advisor calls async — you keep coding; Patronus surfaces its flags in the plan panel when they're ready. Target: opt-in via `[advisor]` config block.
- **`web_search` tool** — currently a stub, implementation in progress.
- **LSP integration** — `internal/lsp/` has the client interface; wiring into the context builder for symbol-aware injection is on the roadmap.

---

## License

See `LICENSE` (or add one).

---

## Contributing

Forge is in active development. Architecture docs: `docs/ARCHITECTURE.md`. Agent guidelines: `AGENTS.md`.

Build & test:
```bash
go build ./...
go test ./...
```

The TUI tests live under `internal/tui/*_test.go` and cover the streaming, lane, and render-cache paths.
