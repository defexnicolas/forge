package tui

type commandDescriptor struct {
	Name        string
	Usage       string
	Description string
}

var tuiCommands = []commandDescriptor{
	{Name: "/help", Usage: "/help", Description: "show this help"},
	{Name: "/dir", Usage: "/dir", Description: "show working directory"},
	{Name: "/theme", Usage: "/theme <name>", Description: "switch theme (default/light/ocean/mono)"},
	{Name: "/model", Usage: "/model [list|set]", Description: "manage models"},
	{Name: "/provider", Usage: "/provider", Description: "configure provider URL, key, model"},
	{Name: "/mode", Usage: "/mode [name]", Description: "switch mode (build/plan/explore/review)"},
	{Name: "/agents", Usage: "/agents", Description: "list subagents"},
	{Name: "/agent", Usage: "/agent <name> <task>", Description: "run a subagent task"},
	{Name: "/plan", Usage: "/plan", Description: "toggle plan panel"},
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
	{Name: "/yarn", Usage: "/yarn [settings|profiles|profile|dry-run|graph|inspect]", Description: "inspect and configure YARN context"},
	{Name: "/skills", Usage: "/skills [repo|refresh [repo]|cache]", Description: "browse, cache, and install skills"},
	{Name: "/tools", Usage: "/tools", Description: "list registered tools"},
	{Name: "/mcp", Usage: "/mcp", Description: "show MCP servers and tools"},
	{Name: "/plugins", Usage: "/plugins", Description: "list discovered plugins"},
	{Name: "/hooks", Usage: "/hooks", Description: "list loaded hooks"},
	{Name: "/session", Usage: "/session", Description: "show current session info"},
	{Name: "/sessions", Usage: "/sessions", Description: "list recent sessions"},
	{Name: "/resume", Usage: "/resume <id|latest>", Description: "resume a session"},
	{Name: "/think", Usage: "/think [on|off]", Description: "toggle thinking visibility"},
	{Name: "/copy", Usage: "/copy", Description: "copy last agent response"},
	{Name: "/status", Usage: "/status", Description: "show current runtime status"},
	{Name: "/config", Usage: "/config", Description: "show effective repo config"},
	{Name: "/review", Usage: "/review", Description: "switch to review mode and show diff"},
	{Name: "/quit", Usage: "/quit", Description: "exit forge and save history"},
}

func slashCommandNames() []string {
	names := make([]string, 0, len(tuiCommands))
	for _, cmd := range tuiCommands {
		names = append(names, cmd.Name)
	}
	return names
}
