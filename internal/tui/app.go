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
	"forge/internal/projectstate"
	"forge/internal/session"
	"forge/internal/skills"
	"forge/internal/tools"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type Options struct {
	CWD          string
	Config       config.Config
	Tools        *tools.Registry
	Providers    *llm.Registry
	Session      *session.Store
	Skills       *skills.Manager
	Plugins      *plugins.Manager
	MCP          *mcp.Manager
	Hooks        *hooks.Runner
	ProjectState *projectstate.Service
}

type App struct{ options Options }

func New(options Options) *App { return &App{options: options} }

func (a *App) Run(ctx context.Context) error {
	// No mouse capture — keeps terminal text selection / copy-paste working natively.
	program := tea.NewProgram(newModel(a.options), tea.WithContext(ctx))
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
	formModelMulti
	formConfirmExecute
	formConfirmExplorerPlan
	formYarnSettings
	formYarnMenu
	formApproval
	formAskUser
	formConfirmPlanReset
)

var lastAgentResponse string

const viewportInputGapLines = 2
const inputMinLines = 1
const inputMaxLines = 6
const pasteGuardDuration = 180 * time.Millisecond
const pasteBurstThreshold = 20 * time.Millisecond

type model struct {
	options                Options
	agentRuntime           *agent.Runtime
	agentEvents            <-chan agent.Event
	pendingApproval        *agent.ApprovalRequest
	pendingAskUser         *agent.AskUserRequest
	agentRunning           bool
	streaming              bool
	quitting               bool
	thinkEnabled           bool
	showPlan               bool
	searching              bool
	searchMode             searchMode
	suggestions            []string
	suggestionIdx          int
	activeForm             formMode
	providerForm           providerForm
	skillsForm             skillsForm
	themeForm              themeForm
	modelForm              modelForm
	modelMultiForm         modelMultiForm
	confirmExecute         confirmForm
	confirmExplorerPlan    confirmForm
	confirmPlanReset       confirmForm
	pendingPlanLine        string
	yarnSettingsForm       yarnSettingsForm
	yarnMenuForm           yarnMenuForm
	approvalForm           approvalForm
	askUserForm            askUserForm
	currentAssistant       *strings.Builder
	modelProgress          *agent.ModelProgress
	pendingExecuteLine     string
	pendingExplorerHandoff string
	lastBuildPreflight     string
	streamingStartIdx      int
	// streamingBuilder holds the full indented text of the assistant line
	// currently being streamed. Replaces the O(n²) `m.history[last] += delta`
	// concat — we append to the builder on every delta and materialize the
	// single-string result into m.history at each flush tick.
	streamingBuilder   strings.Builder
	streamingRaw       strings.Builder
	streamFlushPending bool
	// prefixRendered caches strings.Join(m.history[:streamingStartIdx], "\n")
	// so refreshStreaming can skip rejoining the entire history on every flush.
	// Rebuilt lazily when prefixDirty is true.
	prefixRendered string
	prefixDirty    bool
	pendingCommand tea.Cmd
	btwEvents              <-chan agent.Event
	btwStreaming           bool
	remoteServer           *remoteControlHandle
	forceScrollBottom      bool
	stickyBottom           bool
	lastEscTime            time.Time
	lastRuneInputAt        time.Time
	pasteGuardUntil        time.Time
	width                  int
	height                 int
	input                  textarea.Model
	viewport               viewport.Model
	history                []string
	theme                  Theme
	// Tool-call collapsing: after the first couple of tool uses in a turn,
	// subsequent ones fold into a single "+N more tool uses" line so the
	// viewport doesn't drown in read_file/search noise.
	toolUsesInTurn       int
	collapsedToolLineIdx int
	lastToolCollapsed    bool
	// turnToolActivity accumulates tool calls (name + key input) for the
	// current turn so explore→plan handoffs can ship a structured summary
	// instead of just the final assistant text. Reset at turn start.
	turnToolActivity []turnToolEntry
	// turnUserInput captures the user's message for the current turn so the
	// explore→plan handoff can include the question that produced the
	// findings.
	turnUserInput string
	// markdown renders the assistant's final text through Glamour so fenced
	// code blocks, lists, and emphasis come out syntax-highlighted. Never
	// applied to deltas — streaming stays plain to preserve tk/s throughput.
	markdown *markdownRenderer
	// laneGroup tracks the live state of the current spawn_subagents batch.
	// When an EventSubagentProgress arrives for a new batch id, a fresh
	// group is spliced into m.history; subsequent events rewrite those
	// lines in place so the user sees lane status evolve rather than a
	// new block per tick.
	laneGroup *laneGroup
}

type turnToolEntry struct {
	Name   string
	Input  string
	Result string
}

func newModel(options Options) model {
	input := textarea.New()
	input.Placeholder = "Ask Forge... (/help for commands, @file to attach)"
	input.Focus()
	input.CharLimit = 4096
	input.SetWidth(96)
	input.SetHeight(inputMinLines)
	input.ShowLineNumbers = false
	input.Prompt = ""

	vp := viewport.New(100, 24)
	theme := DefaultTheme()

	runtime := agent.NewRuntime(options.CWD, options.Config, options.Tools, options.Providers)
	runtime.Builder.History = options.Session
	runtime.Builder.Skills = options.Skills
	runtime.Builder.ProjectState = options.ProjectState
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
		options:              options,
		agentRuntime:         runtime,
		input:                input,
		viewport:             vp,
		width:                100,
		height:               33,
		thinkEnabled:         true,
		theme:                theme,
		currentAssistant:     &strings.Builder{},
		collapsedToolLineIdx: -1,
		streamingStartIdx:    -1,
		stickyBottom:         true,
		markdown:             newMarkdownRenderer(100, theme.Name),
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

func (m model) Init() tea.Cmd { return textarea.Blink }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	// Delegate to active form/search if any.
	if result, cmd, handled := m.handleFormUpdate(msg); handled {
		return result, cmd
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.recalcLayout()
		if m.markdown != nil {
			m.markdown.Resize(m.viewport.Width, m.theme.Name)
		}
		m.refresh()
	case agentEventMsg:
		switch msg.event.Type {
		case agent.EventAssistantDelta:
			// Hot path during local (Ollama) streaming. Append to the
			// streaming builders only — no viewport re-render. A single
			// flush tick at ~30fps (streamFlushMsg below) materializes the
			// accumulated text into m.history and repaints the viewport.
			m.appendAgentEvent(msg.event)
			if m.streaming && !m.streamFlushPending {
				m.streamFlushPending = true
				cmds = append(cmds, scheduleStreamFlush())
			}
		case agent.EventModelProgress:
			// Progress is rendered in the footer (modelProgressView) which
			// is drawn by View() after every Update — no viewport refresh
			// is needed, and on streaming responses these events fire per
			// chunk so a full refresh here was a pure tax.
			m.appendAgentEvent(msg.event)
		case agent.EventClearStreaming:
			// Streamed block is being discarded (a <tool_call> was detected
			// mid-stream). Do NOT flush the pending builder — its contents
			// are exactly what we need to throw away.
			m.appendAgentEvent(msg.event)
			m.refresh()
		default:
			// Any structural event (tool call, tool result, assistant text,
			// done, error, etc.) — materialize any pending streaming delta
			// first so the mutation sees up-to-date history.
			m.flushStreaming()
			m.appendAgentEvent(msg.event)
			m.refresh()
		}
		if msg.event.Type == agent.EventDone {
			m.agentRunning = false
			m.agentEvents = nil
			break
		}
		cmds = append(cmds, waitForAgentEvent(msg.events))
	case streamFlushMsg:
		m.streamFlushPending = false
		if m.streaming {
			m.flushStreaming()
			m.refreshStreaming()
			m.streamFlushPending = true
			cmds = append(cmds, scheduleStreamFlush())
		}
	case btwEventMsg:
		m.appendBtwEvent(msg.event)
		m.refresh()
		if msg.event.Type == agent.EventDone {
			m.btwEvents = nil
			break
		}
		cmds = append(cmds, waitForBtwEvent(msg.events))
	case remoteInputMsg:
		// A web client submitted a prompt or /command. Treat it exactly like
		// keyboard input and keep pumping for the next one.
		next := m.handleLine(msg.Text)
		if m.remoteServer != nil {
			cmds = append(cmds, pumpRemoteInputs(context.Background(), m.remoteServer.server.Inputs()))
		}
		if next != nil {
			cmds = append(cmds, next)
		}
		m.refresh()
	case tea.KeyMsg:
		now := time.Now()
		m.updatePasteGuard(msg, now)
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
		case tea.KeyCtrlT:
			// Toggle thinking visibility live. Applies to the next rendered
			// assistant block — historical turns keep their original box/no-box.
			m.thinkEnabled = !m.thinkEnabled
			m.refresh()
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
			if now.Before(m.pasteGuardUntil) {
				m.pasteGuardUntil = now.Add(pasteGuardDuration)
				break
			}
			line := strings.TrimSpace(m.input.Value())
			m.input.SetValue("")
			if line != "" {
				m.stickyBottom = true
				cmds = append(cmds, m.handleLine(line))
				m.refresh()
			}
			return m, tea.Batch(cmds...)
		case tea.KeyPgUp:
			m.viewport.ViewUp()
			m.stickyBottom = m.viewport.AtBottom()
			return m, nil
		case tea.KeyPgDown:
			m.viewport.ViewDown()
			m.stickyBottom = m.viewport.AtBottom()
			return m, nil
		case tea.KeyUp:
			// Scroll up when input is empty, otherwise let textinput handle it.
			if m.input.Value() == "" {
				m.viewport.LineUp(3)
				m.stickyBottom = m.viewport.AtBottom()
				return m, nil
			}
		case tea.KeyDown:
			// Scroll down when input is empty, otherwise let textinput handle it.
			if m.input.Value() == "" {
				m.viewport.LineDown(3)
				m.stickyBottom = m.viewport.AtBottom()
				return m, nil
			}
		}
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	cmds = append(cmds, cmd)
	// Don't forward KeyMsg to viewport — it grabs scroll keys and causes jitter while typing.
	if _, isKey := msg.(tea.KeyMsg); !isKey {
		m.viewport, cmd = m.viewport.Update(msg)
		cmds = append(cmds, cmd)
	}

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

	m.recalcLayout()
	return m, tea.Batch(cmds...)
}

func (m *model) updatePasteGuard(msg tea.KeyMsg, now time.Time) {
	if msg.Type == tea.KeyRunes {
		if msg.Paste || len(msg.Runes) > 1 || (!m.lastRuneInputAt.IsZero() && now.Sub(m.lastRuneInputAt) <= pasteBurstThreshold) {
			m.pasteGuardUntil = now.Add(pasteGuardDuration)
		}
		m.lastRuneInputAt = now
		return
	}
	if msg.String() == "ctrl+v" {
		m.pasteGuardUntil = now.Add(pasteGuardDuration)
	}
}

func (m model) View() string {
	t := m.theme
	inputArea := m.inputAreaView()
	statusLine := m.statusLineView()

	// Approval takes over the chat area (not an overlay below) — the user
	// asked for a momentary full-screen replacement so the decision is
	// unmissable.
	if m.activeForm == formApproval {
		return m.approvalForm.View() + "\n\n" + statusLine
	}
	if m.activeForm == formAskUser {
		return m.viewport.View() + "\n" + m.askUserForm.View() + "\n\n" + statusLine
	}

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
	if m.activeForm == formModelMulti {
		return m.viewport.View() + "\n" + m.modelMultiForm.View() + "\n\n" + statusLine
	}
	if m.activeForm == formConfirmExecute {
		return m.viewport.View() + "\n" + m.confirmExecute.View() + "\n\n" + statusLine
	}
	if m.activeForm == formConfirmPlanReset {
		return m.viewport.View() + "\n" + m.confirmPlanReset.View() + "\n\n" + statusLine
	}
	if m.activeForm == formConfirmExplorerPlan {
		return m.viewport.View() + "\n" + m.confirmExplorerPlan.View() + "\n\n" + statusLine
	}
	if m.activeForm == formYarnSettings {
		return m.viewport.View() + "\n" + m.yarnSettingsForm.View() + "\n\n" + statusLine
	}
	if m.activeForm == formYarnMenu {
		return m.viewport.View() + "\n" + m.yarnMenuForm.View() + "\n\n" + statusLine
	}

	// Plan panel on the right side.
	chatArea := m.viewport.View()
	if m.showPlan && m.agentRuntime.Tasks != nil {
		list, err := m.agentRuntime.Tasks.List()
		hasPlan := false
		if m.agentRuntime.Plans != nil {
			if _, ok, _ := m.agentRuntime.Plans.Current(); ok {
				hasPlan = true
			}
		}
		// Hide the panel only when the plan is genuinely empty — previously we
		// also hid it when nothing was "pending", which made the panel vanish
		// after a few approvals even though there were in_progress tasks.
		if err != nil || (len(list) == 0 && !hasPlan) {
			m.showPlan = false
			m.recalcLayout()
		} else {
			vpHeight := m.viewport.Height
			if vpHeight <= 0 {
				vpHeight = 20
			}
			panel := RenderPlanPanel(list, hasPlan, vpHeight, t)
			chatArea = lipgloss.JoinHorizontal(lipgloss.Top, chatArea, "  ", panel)
		}
	}

	if m.searching {
		return chatArea + "\n" + m.searchMode.View(t) + "\n\n" + statusLine
	}

	// Render autocomplete suggestions below input — flow horizontally, wrapping by width.
	if len(m.suggestions) > 0 {
		return chatArea + inputArea + "\n" + m.suggestionView() + "\n\n" + statusLine
	}
	return chatArea + inputArea + "\n\n" + statusLine
}

func (m model) safeWidth() int {
	if m.width > 0 {
		return m.width
	}
	return 80
}

func (m model) inputBoxView() string {
	return m.theme.InputBorder.Width(max(20, m.safeWidth()-4)).Render(m.input.View())
}

func (m model) inputAreaView() string {
	if progress := m.modelProgressView(); progress != "" {
		return "\n" + progress + "\n" + m.inputBoxView()
	}
	return viewportInputGap() + m.inputBoxView()
}

func (m model) modelProgressView() string {
	if m.modelProgress == nil {
		return ""
	}
	p := *m.modelProgress
	if p.Done && !m.agentRunning {
		return ""
	}
	phase := strings.TrimSpace(p.Phase)
	if phase == "" {
		phase = "thinking"
	}
	if p.Step <= 0 {
		p.Step = 1
	}
	elapsed := p.Elapsed.Truncate(100 * time.Millisecond)
	if elapsed < 0 {
		elapsed = 0
	}
	line := fmt.Sprintf("  * %s | step:%d | in:%s out:%s total:%s | %.1f tk/s | %s",
		phase,
		p.Step,
		formatTokenCount(p.InputTokens),
		formatTokenCount(p.OutputTokens),
		formatTokenCount(p.TotalTokens),
		p.TokensPerSecond,
		elapsed,
	)
	maxWidth := max(20, m.safeWidth()-4)
	if len(stripAnsi(line)) > maxWidth {
		line = truncate(line, maxWidth)
	}
	return m.theme.Muted.Render(line)
}

func formatTokenCount(tokens int) string {
	if tokens < 0 {
		tokens = 0
	}
	if tokens < 1000 {
		return fmt.Sprintf("%d", tokens)
	}
	return fmt.Sprintf("%.1fk", float64(tokens)/1000)
}

func (m model) statusLineView() string {
	t := m.theme
	modeName := strings.ToUpper(m.agentRuntime.Mode)
	var mode string
	switch m.agentRuntime.Mode {
	case "plan":
		mode = t.Warning.Render("[" + modeName + "]")
	case "explore":
		mode = t.Accent.Render("[" + modeName + "]")
	default:
		mode = t.StatusValue.Render("[" + modeName + "]")
	}
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
		status = t.ApprovalStyle.Render("awaiting approval")
	} else if m.streaming {
		status = t.StatusActive.Render("streaming")
	} else if m.agentRunning {
		status = t.Warning.Render("thinking")
	}

	tokensUsed := m.agentRuntime.LastTokensUsed
	yarnBudget := m.agentRuntime.LastTokensBudget
	modelWindow, ctxBudget, _ := config.EffectiveBudgets(m.options.Config)
	contextInfo := t.Muted.Render("ctx:--")
	// Color and reference cap pick the self-imposed budget over the raw model
	// window when available — that's the threshold we actually manage to.
	// Typical OpenCode per-turn injection lives in the 10–16k range (tool
	// descriptions + skills XML); our budget default is 8k. When we're well
	// under budget the "lean" check flips to a green marker so the moat is
	// visible in the status bar, not just in theory.
	referenceCap := modelWindow
	if ctxBudget > 0 && ctxBudget < referenceCap {
		referenceCap = ctxBudget
	}
	if referenceCap > 0 {
		pct := (tokensUsed * 100) / referenceCap
		ctxStyle := t.Muted
		if pct > 80 {
			ctxStyle = t.Warning
		}
		if pct > 100 {
			ctxStyle = t.ErrorStyle
		}
		contextInfo = ctxStyle.Render(fmt.Sprintf("ctx:%s/%dk", formatTokenCount(tokensUsed), referenceCap/1000))
		leanThreshold := referenceCap * 60 / 100
		if tokensUsed > 0 && tokensUsed <= leanThreshold {
			contextInfo += " " + t.Success.Render("lean✓")
		} else if pct > 100 {
			contextInfo += " " + t.ErrorStyle.Render("over!")
		}
		if yarnBudget > 0 {
			contextInfo += t.Muted.Render(fmt.Sprintf(" yarn:%dk", yarnBudget/1000))
		}
	} else if yarnBudget > 0 {
		pct := (tokensUsed * 100) / yarnBudget
		ctxStyle := t.Muted
		if pct > 80 {
			ctxStyle = t.Warning
		}
		if pct > 95 {
			ctxStyle = t.ErrorStyle
		}
		contextInfo = ctxStyle.Render(fmt.Sprintf("yarn:%s/%dk", formatTokenCount(tokensUsed), yarnBudget/1000))
	}

	thinkLabel := t.Muted.Render("Think:OFF")
	if m.thinkEnabled {
		thinkLabel = t.StatusActive.Render("Think:ON")
	}
	modelMultiLabel := t.Muted.Render("Multi:OFF")
	if m.options.Config.ModelLoading.Enabled {
		strategy := strings.ToUpper(strings.TrimSpace(m.options.Config.ModelLoading.Strategy))
		if strategy == "" {
			strategy = "ON"
		}
		modelMultiLabel = t.StatusActive.Render("Multi:" + strategy)
	}

	sep := t.Muted.Render(" | ")
	bar := " " + mode + sep +
		t.StatusValue.Render(modelName) + sep +
		thinkLabel + sep +
		modelMultiLabel + sep +
		t.Accent.Render(provider) + sep +
		status + sep +
		contextInfo + sep +
		t.Muted.Render(cwd)
	return t.StatusBar.Render(bar)
}

func (m model) suggestionView() string {
	maxLineWidth := m.safeWidth() - 4
	if maxLineWidth < 20 {
		maxLineWidth = 20
	}
	var lines []string
	var cur strings.Builder
	curLen := 0
	for i, s := range m.suggestions {
		marker := "  "
		if i == m.suggestionIdx {
			marker = m.theme.IndicatorAgent.Render("> ")
		}
		item := marker + m.theme.StatusValue.Render(s)
		itemLen := len("  " + s) // approximate visible width
		if curLen > 0 && curLen+itemLen+2 > maxLineWidth {
			lines = append(lines, cur.String())
			cur.Reset()
			curLen = 0
		}
		if curLen > 0 {
			cur.WriteString("  ")
			curLen += 2
		}
		cur.WriteString(item)
		curLen += itemLen
	}
	if cur.Len() > 0 {
		lines = append(lines, cur.String())
	}
	return strings.Join(lines, "\n")
}

func viewportInputGap() string {
	return strings.Repeat("\n", viewportInputGapLines)
}

func (m *model) handleLine(line string) tea.Cmd {
	t := m.theme
	m.history = append(m.history, "")
	m.history = append(m.history, t.IndicatorUser.Render("* ")+t.UserPrefix.Render("you > ")+line)
	if m.options.Session != nil {
		_ = m.options.Session.LogUser(line)
	}
	// Reset per-turn assistant accumulator for chat.md transcript.
	m.currentAssistant.Reset()
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
	// If in plan mode with tasks and the user wants to execute, route the
	// message straight through — the planner will now dispatch each task to
	// the builder subagent via execute_task, so no mode switch is needed.
	m.history = append(m.history, t.SeparatorLine(m.width-4))
	m.history = append(m.history, t.IndicatorAgent.Render("* ")+t.AgentPrefix.Render("forge"))
	m.history = append(m.history, "")
	m.modelProgress = nil
	// Reset per-turn capture buffers so the explore→plan handoff reflects
	// this turn's activity only.
	m.turnToolActivity = nil
	m.turnUserInput = line
	switch m.agentRuntime.Mode {
	case "explore":
		if preflight := m.runModePreflight("explore", line); strings.TrimSpace(preflight) != "" {
			m.agentRuntime.PendingExplorePreflight = preflight
			m.history = append(m.history, "    "+t.Muted.Render("Explore preflight complete."))
		}
	case "plan":
		if preflight := m.runModePreflight("plan", line); strings.TrimSpace(preflight) != "" {
			m.agentRuntime.PendingExplorerContext = preflight
			m.history = append(m.history, "    "+t.Muted.Render("Plan preflight complete."))
		}
	}
	// In plan mode, wrap every user message with the interview prompt so the
	// model runs the ask_user → plan_write → todo_write flow reliably — not
	// just on /plan new. When a prior plan exists, pop a confirm first so the
	// user decides explicitly between a fresh plan and refining the existing
	// one (previously we silently refined, which appended to the old todos).
	if m.agentRuntime.Mode == "plan" {
		hasPlan := false
		if m.agentRuntime.Plans != nil {
			if _, ok, _ := m.agentRuntime.Plans.Current(); ok {
				hasPlan = true
			}
		}
		if hasPlan {
			m.pendingPlanLine = line
			m.activeForm = formConfirmPlanReset
			m.confirmPlanReset = newConfirmFormWithDefault("A prior plan exists. Clear it and start fresh?", m.theme, false)
			return nil
		}
		m.agentEvents = m.agentRuntime.Run(context.Background(), planInterviewPrompt(line, true))
		m.agentRunning = true
		return waitForAgentEvent(m.agentEvents)
	}
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
	case "/model-multi":
		if len(fields) >= 2 {
			switch fields[1] {
			case "off":
				m.options.Config.ModelLoading.Enabled = false
				m.persistConfig()
				m.syncRuntimeConfig()
				return m.theme.Success.Render("Model multi: OFF")
			default:
				return "Usage: /model-multi [off]"
			}
		}
		m.activeForm = formModelMulti
		m.modelMultiForm = newModelMultiForm(m.options.CWD, m.options.Config, m.options.Providers, m.theme)
		return "Opening model multi selector..."
	case "/agents":
		return m.describeAgents()
	case "/agent":
		if len(fields) < 3 {
			return "Usage: /agent <explorer|reviewer|tester> <task>"
		}
		return m.runSubagentCommand(fields[1], strings.Join(fields[2:], " "))
	case "/plan-new":
		return m.handlePlanCommand(append([]string{"/plan", "new"}, fields[1:]...))
	case "/plan":
		return m.handlePlanCommand(fields)
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
	case "/log":
		return m.describeLog()
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
		goal := ""
		if len(fields) > 2 {
			goal = strings.Join(fields[2:], " ")
		}
		return m.setMode(fields[1], goal)
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
	case "/analyze":
		return m.handleAnalyzeCommand(fields)
	case "/btw":
		if len(fields) < 2 {
			return "Usage: /btw <question>"
		}
		return m.handleBtwCommand(strings.Join(fields[1:], " "))
	case "/remote-control", "/remote":
		return m.handleRemoteCommand(fields)
	default:
		return "Unknown command. Try /help."
	}
}

func (m model) helpText() string {
	t := m.theme
	rows := make([][]string, 0, len(tuiCommands))
	for _, cmd := range tuiCommands {
		subcommands := ""
		if len(cmd.Subcommands) > 0 {
			subcommands = strings.Join(cmd.Subcommands, ", ")
		}
		rows = append(rows, []string{cmd.Usage, cmd.Description, subcommands})
	}
	return t.FormatTable([]string{"Command", "Description", "Subcommands"}, rows)
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
			if next == "explore" && m.showPlan {
				m.showPlan = false
				m.recalcLayout()
			}
			// Mode is reflected in the status bar — don't pollute the viewport.
			return
		}
	}
}

func (m *model) refresh() {
	m.recalcLayout()
	content := m.history
	if m.searchMode.query != "" {
		filtered, positions := FilterHistory(content, m.searchMode.query, m.searchMode.currentIdx)
		content = filtered
		if m.searchMode.currentIdx >= len(positions) {
			m.searchMode.currentIdx = 0
		}
		m.searchMode.positions = positions
	}
	// Reserved trailing padding so the last rendered line can never sit flush
	// against the bottom edge of the viewport and appear crowded by the input.
	rendered := strings.Join(content, "\n") + strings.Repeat("\n", viewportInputGapLines)
	// The full rebuild already covers whatever the streaming prefix cache
	// pointed to, so invalidate it — the next refreshStreaming will rebuild.
	m.prefixDirty = true
	// Inherit sticky state from previous position: if the viewport was already
	// at the bottom, keep it pinned.
	if m.viewport.AtBottom() {
		m.stickyBottom = true
	}
	m.viewport.SetContent(rendered)
	if m.forceScrollBottom {
		m.viewport.GotoBottom()
		m.stickyBottom = true
		m.forceScrollBottom = false
		return
	}
	if m.stickyBottom {
		m.viewport.GotoBottom()
	}
}

// flushStreaming materializes the accumulated streamingBuilder into the
// single history line reserved at streamingStartIdx. Called at ~30fps from
// the streamFlushMsg tick and synchronously before any non-delta event that
// mutates m.history, so downstream event handlers always observe up-to-date
// history. Safe to call when not streaming — no-op.
func (m *model) flushStreaming() {
	if !m.streaming || m.streamingStartIdx < 0 || m.streamingStartIdx >= len(m.history) {
		return
	}
	m.history[m.streamingStartIdx] = m.streamingBuilder.String()
	lastAgentResponse = m.streamingRaw.String()
}

// refreshStreaming paints the viewport during an active streaming response
// without rejoining the entire history. Reuses a cached prefix (everything
// before streamingStartIdx) plus the current streaming line. Falls back to
// the full refresh when search is active (correctness beats throughput in
// that rare case).
func (m *model) refreshStreaming() {
	if !m.streaming || m.searchMode.query != "" {
		m.refresh()
		return
	}
	m.recalcLayout()
	if m.prefixDirty {
		if m.streamingStartIdx > 0 {
			m.prefixRendered = strings.Join(m.history[:m.streamingStartIdx], "\n")
		} else {
			m.prefixRendered = ""
		}
		m.prefixDirty = false
	}
	streamingLine := ""
	if m.streamingStartIdx >= 0 && m.streamingStartIdx < len(m.history) {
		streamingLine = m.history[m.streamingStartIdx]
	}
	var rendered string
	if m.prefixRendered == "" {
		rendered = streamingLine
	} else {
		rendered = m.prefixRendered + "\n" + streamingLine
	}
	rendered += strings.Repeat("\n", viewportInputGapLines)
	if m.viewport.AtBottom() {
		m.stickyBottom = true
	}
	m.viewport.SetContent(rendered)
	if m.forceScrollBottom {
		m.viewport.GotoBottom()
		m.stickyBottom = true
		m.forceScrollBottom = false
		return
	}
	if m.stickyBottom {
		m.viewport.GotoBottom()
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
		w = 80
	}
	m.input.SetWidth(max(20, w-8))
	// Grow textarea with content, capped at inputMaxLines. Must happen before
	// lowerChromeHeight() so the chrome math reflects current input size.
	desired := m.computeInputHeight()
	if m.input.Height() != desired {
		m.input.SetHeight(desired)
	}
	vpWidth := max(20, w-2)
	if m.showPlan {
		vpWidth = max(20, w-planPanelWidth-4)
	}
	m.viewport.Width = vpWidth

	if m.height <= 0 {
		return
	}
	available := m.height - m.lowerChromeHeight()
	if available < 1 {
		available = 1
	}
	m.viewport.Height = available
}

// computeInputHeight returns the textarea height that fits the current content,
// clamped to [inputMinLines, inputMaxLines]. Past the cap, the textarea
// scrolls internally.
func (m model) computeInputHeight() int {
	lines := m.input.LineCount()
	if lines < inputMinLines {
		lines = inputMinLines
	}
	if lines > inputMaxLines {
		lines = inputMaxLines
	}
	return lines
}

func (m model) lowerChromeHeight() int {
	statusHeight := lipgloss.Height(m.statusLineView())
	if m.activeForm == formApproval {
		return 2 + statusHeight
	}
	if view := m.activeFormView(); view != "" {
		return 1 + lipgloss.Height(view) + 2 + statusHeight
	}
	if m.searching {
		return 1 + lipgloss.Height(m.searchMode.View(m.theme)) + 2 + statusHeight
	}

	height := lipgloss.Height(m.inputAreaView())
	if len(m.suggestions) > 0 {
		height += 1 + lipgloss.Height(m.suggestionView())
	}
	return height + 2 + statusHeight
}

func (m model) activeFormView() string {
	switch m.activeForm {
	case formProvider:
		return m.providerForm.View()
	case formSkills:
		return m.skillsForm.View()
	case formTheme:
		return m.themeForm.View()
	case formModel:
		return m.modelForm.View()
	case formModelMulti:
		return m.modelMultiForm.View()
	case formConfirmExecute:
		return m.confirmExecute.View()
	case formConfirmExplorerPlan:
		return m.confirmExplorerPlan.View()
	case formYarnSettings:
		return m.yarnSettingsForm.View()
	case formYarnMenu:
		return m.yarnMenuForm.View()
	default:
		return ""
	}
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
