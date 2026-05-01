package tui

type commandDescriptor struct {
	Name        string
	Usage       string
	Description string
	Subcommands []string
}

var tuiCommands = []commandDescriptor{
	{Name: "/help", Usage: "/help", Description: "show this help"},
	{Name: "/dir", Usage: "/dir", Description: "show working directory"},
	{Name: "/theme", Usage: "/theme <name>", Description: "switch theme (default/light/ocean/mono)"},
	{Name: "/model", Usage: "/model [list|set|reload]", Description: "manage models"},
	{Name: "/model-multi", Usage: "/model-multi [off]", Description: "assign models per agent role", Subcommands: []string{"off"}},
	{Name: "/provider", Usage: "/provider", Description: "configure provider URL, key, model"},
	{Name: "/mode", Usage: "/mode [name]", Description: "switch mode (plan/build/explore)"},
	{Name: "/agents", Usage: "/agents", Description: "list subagents"},
	{Name: "/agent", Usage: "/agent <name> <task>", Description: "run a subagent task"},
	{Name: "/plan", Usage: "/plan [panel|full|todos|new]", Description: "manage plan document and checklist panel", Subcommands: []string{"panel", "full", "todos", "new", "refine"}},
	{Name: "/plan-new", Usage: "/plan-new <goal>", Description: "clear current plan and start a new planning interview"},
	{Name: "/permissions", Usage: "/permissions [set <profile>]", Description: "show/set command permission profile"},
	{Name: "/context", Usage: "/context [pin|drop|yarn|compact]", Description: "show or manage context"},
	{Name: "/pin", Usage: "/pin @path", Description: "pin a context file"},
	{Name: "/drop", Usage: "/drop @path", Description: "drop a pinned context file"},
	{Name: "/diff", Usage: "/diff", Description: "show pending or workspace diff"},
	{Name: "/undo", Usage: "/undo", Description: "undo last approved edit"},
	{Name: "/approve", Usage: "/approve", Description: "approve pending command or edit"},
	{Name: "/reject", Usage: "/reject", Description: "reject pending command or edit"},
	{Name: "/test", Usage: "/test [command]", Description: "run an allowlisted test command"},
	{Name: "/compact", Usage: "/compact", Description: "summarize session into YARN"},
	{
		Name:        "/yarn",
		Usage:       "/yarn [subcommand]",
		Description: "YARN context menu & configuration",
		Subcommands: []string{"settings", "settings set", "profiles", "profile", "dry-run", "graph", "inspect"},
	},
	{Name: "/skills", Usage: "/skills [repo|refresh [repo]|cache]", Description: "browse, cache, and install skills"},
	{Name: "/tools", Usage: "/tools", Description: "list registered tools"},
	{Name: "/mcp", Usage: "/mcp [resources|prompts]", Description: "show MCP servers, tools, resources, or prompts", Subcommands: []string{"resources", "prompts"}},
	{Name: "/plugins", Usage: "/plugins", Description: "list discovered plugins"},
	{Name: "/hooks", Usage: "/hooks", Description: "list loaded hooks"},
	{Name: "/log", Usage: "/log", Description: "show live plain-text log path"},
	{Name: "/session", Usage: "/session", Description: "show current session info"},
	{Name: "/sessions", Usage: "/sessions", Description: "list recent sessions"},
	{Name: "/resume", Usage: "/resume <id|latest>", Description: "resume a session"},
	{Name: "/think", Usage: "/think [on|off]", Description: "toggle thinking visibility"},
	{Name: "/copy", Usage: "/copy", Description: "copy last agent response"},
	{Name: "/status", Usage: "/status", Description: "show current runtime status"},
	{Name: "/config", Usage: "/config", Description: "show effective repo config"},
	{Name: "/review", Usage: "/review", Description: "switch to review mode and show diff"},
	{Name: "/analyze", Usage: "/analyze [refresh|show]", Description: "scan and cache project snapshot", Subcommands: []string{"refresh", "show"}},
	{Name: "/btw", Usage: "/btw <question>", Description: "side LLM call using shared context (parallel)"},
	{Name: "/remote-control", Usage: "/remote-control [start|stop|status] [port]", Description: "serve session over LAN for web/device access", Subcommands: []string{"start", "stop", "status"}},
	{Name: "/quit", Usage: "/quit", Description: "exit forge and save history"},
}

func slashCommandNames() []string {
	names := make([]string, 0, len(tuiCommands))
	for _, cmd := range tuiCommands {
		names = append(names, cmd.Name)
	}
	return names
}

func subcommandsFor(name string) []string {
	for _, cmd := range tuiCommands {
		if cmd.Name == name {
			return cmd.Subcommands
		}
	}
	return nil
}
