package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"forge/internal/agent"
	"forge/internal/config"
	"forge/internal/hooks"
	"forge/internal/llm"
	"forge/internal/mcp"
	"forge/internal/plugins"
	"forge/internal/session"
	"forge/internal/skills"
	"forge/internal/tools"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type Options struct {
	CWD       string
	Config    config.Config
	Tools     *tools.Registry
	Providers *llm.Registry
	Session   *session.Store
	Skills    *skills.Manager
	Plugins   *plugins.Manager
	MCP       *mcp.Manager
	Hooks     *hooks.Runner
}

type App struct{ options Options }

func New(options Options) *App { return &App{options: options} }

func (a *App) Run(ctx context.Context) error {
	program := tea.NewProgram(newModel(a.options), tea.WithContext(ctx), tea.WithMouseCellMotion())
	_, err := program.Run()
	return err
}

type formMode int

const (
	formNone formMode = iota
	formProvider
	formSkills
	formTheme
	formModel
	formConfirmExecute
	formYarnSettings
)

var lastAgentResponse string

type model struct {
	options            Options
	agentRuntime       *agent.Runtime
	agentEvents        <-chan agent.Event
	pendingApproval    *agent.ApprovalRequest
	pendingAskUser     *agent.AskUserRequest
	agentRunning       bool
	streaming          bool
	quitting           bool
	thinkEnabled       bool
	showPlan           bool
	searching          bool
	searchMode         searchMode
	suggestions        []string
	suggestionIdx      int
	activeForm         formMode
	providerForm       providerForm
	skillsForm         skillsForm
	themeForm          themeForm
	modelForm          modelForm
	confirmExecute     confirmForm
	yarnSettingsForm   yarnSettingsForm
	pendingExecuteLine string
	pendingCommand     tea.Cmd
	forceScrollBottom  bool
	lastEscTime        time.Time
	width              int
	input              textinput.Model
	viewport           viewport.Model
	history            []string
	theme              Theme
}

func newModel(options Options) model {
	input := textinput.New()
	input.Placeholder = "Ask Forge... (/help for commands, @file to attach)"
	input.Focus()
	input.CharLimit = 4096
	input.Width = 96

	vp := viewport.New(100, 24)
	theme := DefaultTheme()

	runtime := agent.NewRuntime(options.CWD, options.Config, options.Tools, options.Providers)
	runtime.Builder.History = options.Session
	runtime.Builder.Skills = options.Skills
	runtime.Hooks = options.Hooks

	sessionName := "new"
	if options.Session != nil {
		sessionName = options.Session.ID()
		if len(sessionName) > 12 {
			sessionName = sessionName[:12]
		}
	}

	cwd := filepath.Base(options.CWD)

	m := model{
		options:      options,
		agentRuntime: runtime,
		input:        input,
		viewport:     vp,
		width:        100,
		thinkEnabled: true,
		theme:        theme,
		history: []string{
			"",
			theme.Accent.Render("  forge") + theme.Muted.Render(" | "+cwd+" | session:"+sessionName),
			theme.Muted.Render("  /help for commands | Shift+Tab to switch mode | Ctrl+C or /quit to exit"),
			"",
		},
	}
	m.refresh()
	return m
}

func (m model) Init() tea.Cmd { return textinput.Blink }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	// Delegate to active form/search if any.
	if result, cmd, handled := m.handleFormUpdate(msg); handled {
		return result, cmd
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.viewport.Height = max(8, msg.Height-7)
		m.recalcLayout()
		m.refresh()
	case agentEventMsg:
		m.appendAgentEvent(msg.event)
		m.refresh()
		if msg.event.Type == agent.EventDone {
			m.agentRunning = false
			m.agentEvents = nil
			break
		}
		cmds = append(cmds, waitForAgentEvent(msg.events))
	case tea.KeyMsg:
		// Clear suggestions on Enter or Esc; auto-suggest handles the rest.
		if (msg.Type == tea.KeyEnter || msg.Type == tea.KeyEsc) && len(m.suggestions) > 0 {
			m.suggestions = nil
			m.suggestionIdx = 0
		}
		switch msg.Type {
		case tea.KeyCtrlF:
			// Search mode.
			m.searching = true
			m.searchMode = newSearchMode(m.theme)
			m.searchMode.active = true
			m.searchMode.input.Focus()
			m.input.Blur()
			return m, nil
		case tea.KeyEsc:
			// First ESC: dismiss suggestions/forms. Double ESC (within 500ms): quit.
			if len(m.suggestions) > 0 {
				m.suggestions = nil
				m.suggestionIdx = 0
				m.lastEscTime = time.Now()
				return m, nil
			}
			now := time.Now()
			if now.Sub(m.lastEscTime) < 500*time.Millisecond {
				m.dumpHistory()
				return m, tea.Quit
			}
			m.lastEscTime = now
			// Replace previous ESC hint if it exists, instead of appending another.
			escHint := m.theme.Muted.Render("  Press ESC again to quit.")
			if len(m.history) > 0 && m.history[len(m.history)-1] == escHint {
				// Already showing hint; don't duplicate.
			} else {
				m.history = append(m.history, escHint)
			}
			m.refresh()
			return m, nil
		case tea.KeyCtrlC:
			m.dumpHistory()
			return m, tea.Quit
		case tea.KeyShiftTab:
			m.cycleMode()
			m.refresh()
		case tea.KeyTab:
			// Autocomplete: cycle if already showing, else apply first suggestion.
			if len(m.suggestions) > 0 {
				m.suggestionIdx = (m.suggestionIdx + 1) % len(m.suggestions)
				m.input.SetValue(applySuggestion(m.input.Value(), m.suggestions[m.suggestionIdx]))
				m.input.CursorEnd()
			} else {
				val := m.input.Value()
				suggestions := Suggest(val, m.options.CWD)
				if len(suggestions) > 0 {
					m.suggestions = suggestions
					m.suggestionIdx = 0
					m.input.SetValue(applySuggestion(val, suggestions[0]))
					m.input.CursorEnd()
				}
			}
			return m, nil
		case tea.KeyEnter:
			line := strings.TrimSpace(m.input.Value())
			m.input.SetValue("")
			if line != "" {
				cmds = append(cmds, m.handleLine(line))
				m.refresh()
			}
		case tea.KeyPgUp:
			m.viewport.ViewUp()
			return m, nil
		case tea.KeyPgDown:
			m.viewport.ViewDown()
			return m, nil
		case tea.KeyUp:
			// Scroll up when input is empty, otherwise let textinput handle it.
			if m.input.Value() == "" {
				m.viewport.LineUp(3)
				return m, nil
			}
		case tea.KeyDown:
			// Scroll down when input is empty, otherwise let textinput handle it.
			if m.input.Value() == "" {
				m.viewport.LineDown(3)
				return m, nil
			}
		}
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	cmds = append(cmds, cmd)
	m.viewport, cmd = m.viewport.Update(msg)
	cmds = append(cmds, cmd)

	// Auto-suggest as user types / or @.
	val := m.input.Value()
	if strings.HasPrefix(val, "/") || strings.Contains(val, "@") {
		newSuggestions := Suggest(val, m.options.CWD)
		if len(newSuggestions) > 0 {
			m.suggestions = newSuggestions
			m.suggestionIdx = 0
		} else {
			m.suggestions = nil
		}
	}

	return m, tea.Batch(cmds...)
}

func (m model) View() string {
	t := m.theme
	w := m.width
	if w <= 0 {
		w = 80
	}

	// Input box with border.
	inputBox := t.InputBorder.Width(w - 4).Render(m.input.View())

	// Status bar.
	mode := t.StatusValue.Render(m.agentRuntime.Mode)
	modelName := m.agentRuntime.LastModelUsed
	if modelName == "" {
		modelName = m.options.Config.Models["chat"]
	}
	if modelName == "" {
		modelName = "default"
	}
	cwd := filepath.Base(m.options.CWD)
	provider := m.options.Config.Providers.Default.Name
	status := t.Muted.Render("idle")
	if m.pendingAskUser != nil {
		status = t.ApprovalStyle.Render("? type your answer")
	} else if m.pendingApproval != nil {
		status = t.ApprovalStyle.Render("approval: /approve or /reject")
	} else if m.streaming {
		status = t.StatusActive.Render("streaming")
	} else if m.agentRunning {
		status = t.Warning.Render("thinking")
	}

	// Context token usage.
	tokensUsed := m.agentRuntime.LastTokensUsed
	tokensBudget := m.agentRuntime.LastTokensBudget
	contextInfo := t.Muted.Render("ctx:--")
	if tokensBudget > 0 {
		pct := 0
		if tokensBudget > 0 {
			pct = (tokensUsed * 100) / tokensBudget
		}
		ctxStyle := t.Muted
		if pct > 80 {
			ctxStyle = t.Warning
		}
		if pct > 95 {
			ctxStyle = t.ErrorStyle
		}
		contextInfo = ctxStyle.Render(fmt.Sprintf("ctx:%dk/%dk", tokensUsed/1000, tokensBudget/1000))
	}

	thinkLabel := t.Muted.Render("Think:OFF")
	if m.thinkEnabled {
		thinkLabel = t.StatusActive.Render("Think:ON")
	}

	sep := t.Muted.Render(" | ")
	bar := " " + mode + sep +
		t.StatusValue.Render(modelName) + sep +
		thinkLabel + sep +
		t.Accent.Render(provider) + sep +
		status + sep +
		contextInfo + sep +
		t.Muted.Render(cwd)
	statusLine := t.StatusBar.Render(bar)

	// Form overlays.
	if m.activeForm == formProvider {
		return m.viewport.View() + "\n" + m.providerForm.View() + "\n\n" + statusLine
	}
	if m.activeForm == formSkills {
		return m.viewport.View() + "\n" + m.skillsForm.View() + "\n\n" + statusLine
	}
	if m.activeForm == formTheme {
		return m.viewport.View() + "\n" + m.themeForm.View() + "\n\n" + statusLine
	}
	if m.activeForm == formModel {
		return m.viewport.View() + "\n" + m.modelForm.View() + "\n\n" + statusLine
	}
	if m.activeForm == formConfirmExecute {
		return m.viewport.View() + "\n" + m.confirmExecute.View() + "\n\n" + statusLine
	}
	if m.activeForm == formYarnSettings {
		return m.viewport.View() + "\n" + m.yarnSettingsForm.View() + "\n\n" + statusLine
	}

	// Plan panel on the right side.
	chatArea := m.viewport.View()
	if m.showPlan && m.agentRuntime.Tasks != nil {
		list, err := m.agentRuntime.Tasks.List()
		if err == nil && len(list) > 0 {
			// Auto-hide if all tasks are completed.
			allDone := true
			for _, task := range list {
				if task.Status != "completed" && task.Status != "done" {
					allDone = false
					break
				}
			}
			if allDone {
				m.showPlan = false
				m.recalcLayout()
			} else {
				vpHeight := m.viewport.Height
				if vpHeight <= 0 {
					vpHeight = 20
				}
				panel := RenderPlanPanel(list, vpHeight, t)
				chatArea = lipgloss.JoinHorizontal(lipgloss.Top, chatArea, "  ", panel)
			}
		}
	}

	if m.searching {
		return chatArea + "\n" + m.searchMode.View(t) + "\n\n" + statusLine
	}

	// Render autocomplete suggestions below input.
	if len(m.suggestions) > 0 {
		var sugLines []string
		for i, s := range m.suggestions {
			marker := "  "
			if i == m.suggestionIdx {
				marker = t.IndicatorAgent.Render("> ")
			}
			sugLines = append(sugLines, marker+t.StatusValue.Render(s))
		}
		suggestionView := strings.Join(sugLines, "\n")
		return chatArea + "\n" + inputBox + "\n" + suggestionView + "\n\n" + statusLine
	}
	return chatArea + "\n" + inputBox + "\n\n" + statusLine
}

func (m *model) handleLine(line string) tea.Cmd {
	t := m.theme
	m.history = append(m.history, "")
	m.history = append(m.history, t.IndicatorUser.Render("* ")+t.UserPrefix.Render("you > ")+line)
	if m.options.Session != nil {
		_ = m.options.Session.LogUser(line)
	}
	if strings.HasPrefix(line, "/") {
		result := m.handleCommand(line)
		cmd := m.pendingCommand
		m.pendingCommand = nil
		m.history = append(m.history, result)
		if m.options.Session != nil {
			_ = m.options.Session.LogCommand(line, result)
		}
		if m.quitting {
			m.dumpHistory()
			return tea.Quit
		}
		return cmd
	}
	// If the agent is waiting for user input (ask_user tool), send the answer.
	if m.pendingAskUser != nil {
		m.pendingAskUser.Response <- line
		m.history = append(m.history, "    "+t.Muted.Render("→ ")+line)
		m.pendingAskUser = nil
		return nil
	}
	if m.agentRunning {
		m.history = append(m.history, t.Warning.Render("  Agent is still running."))
		return nil
	}
	// If in plan mode with tasks and the user wants to execute, show confirm prompt.
	if m.agentRuntime.Mode == "plan" && looksLikeExecute(line) {
		if tasks, err := m.agentRuntime.Tasks.List(); err == nil && len(tasks) > 0 {
			m.pendingExecuteLine = line
			m.activeForm = formConfirmExecute
			m.confirmExecute = newConfirmForm("Switch to build mode and execute the plan?", m.theme)
			return nil
		}
	}
	m.history = append(m.history, t.SeparatorLine(m.width-4))
	m.history = append(m.history, t.IndicatorAgent.Render("* ")+t.AgentPrefix.Render("forge ["+m.agentRuntime.Mode+"]"))
	m.history = append(m.history, "")
	m.agentEvents = m.agentRuntime.Run(context.Background(), line)
	m.agentRunning = true
	return waitForAgentEvent(m.agentEvents)
}

func (m *model) handleCommand(line string) string {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return ""
	}
	switch fields[0] {
	case "/help":
		return m.helpText()
	case "/quit", "/exit":
		m.quitting = true
		return m.theme.Muted.Render("Goodbye.")
	case "/dir":
		return m.theme.Accent.Render("  CWD: ") + m.options.CWD
	case "/theme":
		if len(fields) >= 2 {
			m.theme = GetTheme(fields[1])
			return m.theme.Success.Render("Theme: " + m.theme.Name)
		}
		m.activeForm = formTheme
		m.themeForm = newThemeForm(m.theme)
		return "Opening theme selector..."
	case "/model":
		if len(fields) >= 2 {
			return m.handleModelCommand(fields)
		}
		m.activeForm = formModel
		m.modelForm = newModelForm(m.options.CWD, m.options.Config, m.options.Providers, m.theme)
		return "Opening model selector..."
	case "/agents":
		return m.describeAgents()
	case "/agent":
		if len(fields) < 3 {
			return "Usage: /agent <explorer|reviewer|tester> <task>"
		}
		return m.runSubagentCommand(fields[1], strings.Join(fields[2:], " "))
	case "/plan":
		m.showPlan = !m.showPlan
		m.recalcLayout()
		if m.showPlan {
			return m.theme.Success.Render("Plan panel: ON")
		}
		return m.theme.Muted.Render("Plan panel: OFF")
	case "/tools":
		return m.describeTools()
	case "/status":
		return m.describeStatus()
	case "/config":
		return m.describeConfig()
	case "/review":
		return m.enterReviewMode()
	case "/permissions":
		if len(fields) >= 3 && fields[1] == "set" {
			return m.setPermissionProfile(fields[2])
		}
		return m.describePermissions()
	case "/session":
		return m.describeSession()
	case "/sessions":
		return m.describeSessions()
	case "/resume":
		if len(fields) < 2 {
			return "Usage: /resume <session-id|latest>"
		}
		return m.resumeSession(fields[1])
	case "/yarn":
		return m.handleYarnCommand(fields)
	case "/compact":
		return m.compactSession()
	case "/pin":
		if len(fields) < 2 {
			return "Usage: /pin @path/to/file"
		}
		return m.pinContext(fields[1])
	case "/drop":
		if len(fields) < 2 {
			return "Usage: /drop @path/to/file"
		}
		return m.dropContext(fields[1])
	case "/test":
		command := "go test ./..."
		if len(fields) > 1 {
			command = strings.Join(fields[1:], " ")
		}
		return m.runTestCommand(command)
	case "/skills":
		if m.options.Skills != nil {
			if len(fields) > 1 && fields[1] == "cache" {
				return m.describeSkillsCache(fields[2:])
			}
			m.activeForm = formSkills
			var repos []string
			force := false
			if len(fields) > 1 && fields[1] == "refresh" {
				force = true
				if len(fields) > 2 {
					repos = []string{fields[2]}
				}
			} else if len(fields) > 1 {
				repos = []string{fields[1]}
			}
			m.skillsForm, m.pendingCommand = newSkillsForm(m.options.CWD, m.options.Skills, m.theme, repos, force)
			return "Opening skills browser..."
		}
		return "Skills manager not available."
	case "/plugins":
		return m.describePlugins()
	case "/mcp":
		if m.options.MCP != nil {
			return m.options.MCP.Describe()
		}
		return "MCP not loaded."
	case "/hooks":
		if m.options.Hooks != nil {
			return m.options.Hooks.Describe()
		}
		return "No hooks loaded."
	case "/think":
		if len(fields) >= 2 {
			switch fields[1] {
			case "on":
				m.thinkEnabled = true
				return m.theme.Success.Render("Think mode: ON")
			case "off":
				m.thinkEnabled = false
				return m.theme.Success.Render("Think mode: OFF")
			}
		}
		s := "OFF"
		if m.thinkEnabled {
			s = "ON"
		}
		return "Think: " + s + " | /think on|off"
	case "/provider":
		m.activeForm = formProvider
		m.providerForm = newProviderForm(m.options.CWD, m.options.Config, m.theme)
		return "Opening provider config..."
	case "/mode":
		if len(fields) < 2 {
			return m.describeMode()
		}
		return m.setMode(fields[1])
	case "/copy":
		return m.copyToClipboard()
	case "/diff":
		return m.describeDiff()
	case "/approve":
		return m.approvePending()
	case "/reject":
		return m.rejectPending()
	case "/undo":
		return m.undoLast()
	case "/context":
		return m.handleContextCommand(fields)
	default:
		return "Unknown command. Try /help."
	}
}

func (m model) helpText() string {
	t := m.theme
	rows := make([][]string, 0, len(tuiCommands))
	for _, cmd := range tuiCommands {
		rows = append(rows, []string{cmd.Usage, cmd.Description})
	}
	return t.FormatTable([]string{"Command", "Description"}, rows)
}

func (m model) copyToClipboard() string {
	if lastAgentResponse == "" {
		return m.theme.Warning.Render("Nothing to copy.")
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("clip")
	case "darwin":
		cmd = exec.Command("pbcopy")
	default:
		cmd = exec.Command("xclip", "-selection", "clipboard")
	}
	cmd.Stdin = strings.NewReader(lastAgentResponse)
	if err := cmd.Run(); err != nil {
		return m.theme.ErrorStyle.Render("Copy failed: " + err.Error())
	}
	return m.theme.Success.Render("Copied to clipboard.")
}

func (m *model) cycleMode() {
	modes := agent.ModeNames()
	current := m.agentRuntime.Mode
	for i, name := range modes {
		if name == current {
			next := modes[(i+1)%len(modes)]
			_ = m.agentRuntime.SetMode(next)
			m.history = append(m.history, m.theme.Success.Render("  Mode: "+next))
			return
		}
	}
}

func (m *model) refresh() {
	content := m.history
	if m.searchMode.query != "" {
		filtered, matches := FilterHistory(content, m.searchMode.query)
		content = filtered
		m.searchMode.matches = matches
	}
	// Check if viewport is already at the bottom before updating content.
	wasAtBottom := m.viewport.AtBottom()
	m.viewport.SetContent(strings.Join(content, "\n"))
	// Auto-scroll to bottom when: already at bottom, agent active, or forced.
	if wasAtBottom || m.agentRunning || m.streaming || m.forceScrollBottom {
		m.viewport.GotoBottom()
		m.forceScrollBottom = false
	}
}

// applySuggestion merges a suggestion into the current input.
// For @ mentions, it preserves text before the @. For / commands, it replaces fully.
func applySuggestion(currentInput, suggestion string) string {
	if strings.HasPrefix(suggestion, "@") {
		// Find the last @ in the current input and replace from there.
		atIdx := strings.LastIndex(currentInput, "@")
		if atIdx >= 0 {
			return currentInput[:atIdx] + suggestion
		}
	}
	return suggestion
}

// looksLikeExecute detects when the user wants to run/execute an existing plan.
// Only matches short confirmations, not full new requests.
func looksLikeExecute(line string) bool {
	lower := strings.TrimSpace(strings.ToLower(line))
	// Only trigger on short messages (confirmations, not new detailed requests).
	if len(strings.Fields(lower)) > 6 {
		return false
	}
	keywords := []string{
		"execute", "ejecuta", "run the plan", "build it",
		"hazlo", "procede", "proceed", "do it", "go ahead",
		"yes", "si", "dale", "adelante", "implementa",
		"execute the plan", "run it", "let's go", "start",
	}
	for _, kw := range keywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// recalcLayout recalculates viewport and input widths based on current state.
func (m *model) recalcLayout() {
	w := m.width
	if w <= 0 {
		return
	}
	m.input.Width = max(20, w-8)
	vpWidth := max(20, w-2)
	if m.showPlan {
		vpWidth = max(20, w-planPanelWidth-4)
	}
	m.viewport.Width = vpWidth
}

// dumpHistory writes the conversation history to a text file so it can be reviewed after exit.
func (m *model) dumpHistory() {
	if m.options.Session == nil {
		return
	}
	historyFile := filepath.Join(m.options.Session.Dir(), "history.txt")
	var b strings.Builder
	for _, line := range m.history {
		b.WriteString(stripAnsi(line))
		b.WriteByte('\n')
	}
	_ = os.WriteFile(historyFile, []byte(b.String()), 0o644)
}

// stripAnsi removes ANSI escape sequences from a string.
func stripAnsi(s string) string {
	var b strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '[' {
			// Skip until we hit a letter (the terminator).
			j := i + 2
			for j < len(s) && !((s[j] >= 'A' && s[j] <= 'Z') || (s[j] >= 'a' && s[j] <= 'z')) {
				j++
			}
			if j < len(s) {
				j++ // skip the terminator letter
			}
			i = j
		} else {
			b.WriteByte(s[i])
			i++
		}
	}
	return b.String()
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
