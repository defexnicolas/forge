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
	"forge/internal/claw"
	"forge/internal/config"
	"forge/internal/gitops"
	"forge/internal/hooks"
	"forge/internal/llm"
	"forge/internal/lsp"
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
	CWD            string
	Config         config.Config
	Tools          *tools.Registry
	Providers      *llm.Registry
	Claw           *claw.Service
	Session        *session.Store
	Skills         *skills.Manager
	Plugins        *plugins.Manager
	MCP            *mcp.Manager
	Hooks          *hooks.Runner
	ProjectState   *projectstate.Service
	GitState       gitops.SessionState
	LSP            lsp.Client
	PluginSettings plugins.MergedSettings
	OutputStyles   []plugins.OutputStyle
	// PluginAgents is the agent definitions discovered under each enabled
	// plugin's agents/ directory. They are merged into the runtime's
	// SubagentRegistry so spawn_subagent can target them by name.
	PluginAgents []agent.PluginAgent
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
	formProfile
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
	profileForm            profileForm
	currentAssistant       *strings.Builder
	modelProgress          *agent.ModelProgress
	readBudgetState        *agent.ReadBudgetState
	pendingExecuteLine     string
	pendingExplorerHandoff string
	lastBuildPreflight     string
	streamingStartIdx      int
	// streamingRaw holds the raw token stream of the assistant line currently
	// being streamed. The indented/think-filtered form for the viewport is
	// derived from this at every flush tick — keeping a single source of
	// truth means a Ctrl+T toggle can re-render the same raw bytes through
	// a different filter without replaying the stream.
	//
	// Pointer type, not value: bubbletea's Update receiver is `func (m model)`
	// which copies the entire model struct on every tick. strings.Builder
	// panics with "illegal use of non-zero Builder copied by value" after the
	// first write if held by value, so both builders need to live on the heap.
	streamingRaw       *strings.Builder
	streamFlushPending bool
	// prefixRendered caches strings.Join(m.history[:streamingStartIdx], "\n")
	// so refreshStreaming can skip rejoining the entire history on every flush.
	// Rebuilt lazily when prefixDirty is true.
	prefixRendered string
	prefixDirty    bool
	// fullRenderCache memoizes the fully-joined m.history from the most recent
	// refresh() call, keyed by a cheap fingerprint (length + hash of last
	// lines). Layout-only refreshes (WindowSizeMsg spam during resize, and
	// any refresh triggered without a history mutation) skip the full
	// strings.Join and reuse the cached string.
	fullRenderCache       string
	fullRenderFingerprint uint64
	pendingCommand        tea.Cmd
	// updateRunning prevents overlapping /update invocations in the
	// workspace transcript. Cleared when an updateRunResultMsg arrives.
	updateRunning bool
	btwEvents             <-chan agent.Event
	btwStreaming          bool
	remoteServer          *remoteControlHandle
	forceScrollBottom     bool
	stickyBottom          bool
	lastEscTime           time.Time
	lastRuneInputAt       time.Time
	pasteGuardUntil       time.Time
	// pastes maps a paste id (1-based, monotonic per session) to the
	// raw content the user pasted. The textarea shows
	// "[Pasted text #N +M lines]" while the user composes their
	// message; on submit, expandPastes() swaps every marker back to
	// the original text before handleLine forwards it to the agent.
	// This keeps the input box readable for big pastes without
	// dropping any of the actual content.
	pastes       map[int]pastedBlock
	pasteCounter int
	width                 int
	height                int
	input                 textarea.Model
	viewport              viewport.Model
	history               []string
	theme                 Theme
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

	// Discover user-authored JSON themes in .forge/themes/. Safe to call
	// with a missing directory — the loader silently skips.
	_ = LoadCustomThemes(filepath.Join(options.CWD, ".forge", "themes"))

	vp := viewport.New(100, 24)
	theme := DefaultTheme()

	runtime := agent.NewRuntime(options.CWD, options.Config, options.Tools, options.Providers)
	runtime.Builder.History = options.Session
	runtime.Builder.Skills = options.Skills
	runtime.Builder.ProjectState = options.ProjectState
	if options.LSP != nil {
		runtime.Builder.LSP = options.LSP
	}
	// Plugin-supplied permissions are appended to the active command policy.
	// They never replace the user's profile, only extend it.
	if len(options.PluginSettings.AllowTools) > 0 {
		runtime.Commands.Allow = append(runtime.Commands.Allow, options.PluginSettings.AllowTools...)
	}
	if len(options.PluginSettings.DenyTools) > 0 {
		runtime.Commands.Deny = append(runtime.Commands.Deny, options.PluginSettings.DenyTools...)
	}
	if len(options.PluginSettings.AskTools) > 0 {
		runtime.Commands.Ask = append(runtime.Commands.Ask, options.PluginSettings.AskTools...)
	}
	// Plugin-supplied subagents become first-class spawn_subagent targets.
	if len(options.PluginAgents) > 0 {
		agent.MergePluginAgents(&runtime.Subagents, options.PluginAgents)
	}
	runtime.Hooks = options.Hooks
	runtime.SetGitSessionState(options.GitState)
	if options.Claw != nil {
		options.Claw.SyncRuntime(options.Config, options.Providers, options.Tools)
	}

	sessionName := "new"
	if options.Session != nil {
		sessionName = options.Session.ID()
		if len(sessionName) > 12 {
			sessionName = sessionName[:12]
		}
	}

	cwd := compactDisplayPath(options.CWD)

	m := model{
		options:              options,
		agentRuntime:         runtime,
		input:                input,
		viewport:             vp,
		width:                100,
		height:               33,
		thinkEnabled:         false,
		theme:                theme,
		currentAssistant:     &strings.Builder{},
		streamingRaw:         &strings.Builder{},
		collapsedToolLineIdx: -1,
		streamingStartIdx:    -1,
		stickyBottom:         true,
		markdown:             newMarkdownRenderer(100, theme.Name),
		history: []string{
			"",
			theme.Accent.Render("  forge") + theme.Muted.Render(" | "+cwd+" | session:"+sessionName),
			theme.Muted.Render("  /help for commands | /mode to switch agent mode | Tab switches panes"),
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
				cmds = append(cmds, m.scheduleStreamFlush())
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
	case updateRunResultMsg:
		m.updateRunning = false
		// Reuse the same describer as the Hub flow so the user sees
		// identical wording regardless of where they triggered /update.
		text := (shellModel{theme: m.theme}).describeUpdateRun(msg)
		styled := m.theme.Muted.Render(text)
		if msg.pullErr != nil || msg.buildErr != nil || (!msg.pull.Pulled && msg.pull.DirtyMsg != "") {
			styled = m.theme.ErrorStyle.Render(text)
		} else if msg.pull.Pulled {
			styled = m.theme.Success.Render(text)
		}
		m.history = append(m.history, styled)
		m.refresh()
	case streamFlushMsg:
		m.streamFlushPending = false
		if m.streaming {
			m.flushStreaming()
			m.refreshStreaming()
			m.streamFlushPending = true
			cmds = append(cmds, m.scheduleStreamFlush())
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
		// Collapse multi-line pastes (>= pasteMinLines) into an inline
		// "[Pasted text #N +M lines]" marker before any other key
		// handling sees the message. The original bytes are stashed in
		// m.pastes and expanded back at submit time. Single-line pastes
		// pass through unchanged so editing snippets stays direct.
		// KeyMsg has slice fields (Runes) so we can't compare structs
		// for equality; just unconditionally re-assign — when no
		// rewrite happened interceptPasteKey returns the same value
		// and the assignment is a no-op.
		if k, ok := m.interceptPasteKey(msg).(tea.KeyMsg); ok {
			msg = k
		}
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
			// Toggle thinking visibility live. flushStreaming re-renders the
			// current streaming block from streamingRaw through the new
			// filter, so the toggle is visible immediately even mid-stream
			// instead of applying only to future turns.
			m.thinkEnabled = !m.thinkEnabled
			m.flushStreaming()
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
			// Expand "[Pasted text #N +M lines]" markers back to their
			// original raw content before forwarding to the agent —
			// the marker is for display only.
			line = m.expandPastes(line)
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
	gitBanner := m.gitBannerView()

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
	if m.activeForm == formProfile {
		return m.viewport.View() + "\n" + m.profileForm.View() + "\n\n" + statusLine
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
		return chatArea + gitBanner + "\n" + m.searchMode.View(t) + "\n\n" + statusLine
	}

	// Render autocomplete suggestions below input — flow horizontally, wrapping by width.
	if len(m.suggestions) > 0 {
		return chatArea + gitBanner + inputArea + "\n" + m.suggestionView() + "\n\n" + statusLine
	}
	return chatArea + gitBanner + inputArea + "\n\n" + statusLine
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

func (m model) gitBannerView() string {
	state := m.agentRuntime.GitSessionState()
	if !state.SnapshotRequiredBeforeMutate {
		return ""
	}
	text := strings.TrimSpace(state.BannerText())
	if text == "" {
		return ""
	}
	return "\n" + m.theme.Warning.Render("  ! "+text)
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
	modelName := strings.TrimSpace(m.agentRuntime.LastModelUsed)
	if modelName == "" {
		modelName = m.activeRoleModelID()
	}
	if modelName == "" {
		modelName = "default"
	}
	cwd := compactDisplayPath(m.options.CWD)
	// Provider label: prefer the runtime's resolved backend name (set after
	// the first turn or by hub/workspace mount flows). Fall back to the live
	// provider's BackendName, then to the configured default name. The
	// fallback chain matters because users routinely register llama-server
	// under the "lmstudio" config slot — we want to display "llama-server"
	// regardless of the registry key.
	provider := strings.TrimSpace(m.agentRuntime.LastProviderUsed)
	if provider == "" {
		if p, _, err := m.agentRuntime.ResolveProvider(); err == nil && p != nil {
			if bn, ok := p.(llm.BackendNamer); ok {
				provider = bn.BackendName()
			} else {
				provider = p.Name()
			}
		}
	}
	if provider == "" {
		provider = strings.TrimSpace(m.options.Config.Providers.Default.Name)
	}
	if provider == "" {
		provider = "(none)"
	}
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
	modelWindow, _, _ := config.EffectiveBudgets(m.activeRoleConfig())
	contextInfo := t.Muted.Render("ctx:--")
	gitInfo := t.Muted.Render("git:unknown")
	// Denominator is the loaded model window — what the user actually cares
	// about ("how much room do I have left before the model clamps?"). The
	// self-imposed BudgetTokens cap used to show here, but that's a soft
	// limit users set once in config and don't track turn-to-turn, so it
	// only confused the display when /model-multi loads a profile that
	// changes the window without touching the budget.
	if modelWindow > 0 {
		pct := (tokensUsed * 100) / modelWindow
		ctxStyle := t.Muted
		if pct > 80 {
			ctxStyle = t.Warning
		}
		if pct > 95 {
			ctxStyle = t.ErrorStyle
		}
		contextInfo = ctxStyle.Render(fmt.Sprintf("ctx:%s/%dk", formatTokenCount(tokensUsed), modelWindow/1000))
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
	gitState := m.agentRuntime.GitSessionState()
	switch {
	case !gitState.RepoInitialized:
		gitInfo = t.Warning.Render("git:none")
	case gitState.SnapshotRequiredBeforeMutate:
		gitInfo = t.Warning.Render("git:dirty")
	case gitState.AutoInitialized || gitState.BaselineCreatedThisSession:
		gitInfo = t.StatusActive.Render("git:baseline")
	default:
		gitInfo = t.Muted.Render("git:clean")
	}

	thinkLabel := t.Muted.Render("Think:PEEK")
	if m.thinkEnabled {
		thinkLabel = t.StatusActive.Render("Think:FULL")
	}
	modelMultiLabel := t.Muted.Render("Multi:OFF")
	if m.options.Config.ModelLoading.Enabled {
		strategy := strings.ToUpper(strings.TrimSpace(m.options.Config.ModelLoading.Strategy))
		if strategy == "" {
			strategy = "ON"
		}
		modelMultiLabel = t.StatusActive.Render("Multi:" + strategy)
	}

	profileName := commandProfileName(m.agentRuntime.Commands)
	profileStyle := t.Muted
	switch profileName {
	case "trusted":
		profileStyle = t.StatusActive
	case "yolo":
		profileStyle = t.Warning
	}
	profileLabel := profileStyle.Render("prof:" + profileName)

	sep := t.Muted.Render(" | ")
	bar := " " + mode + sep +
		t.StatusValue.Render(modelName) + sep +
		thinkLabel + sep +
		modelMultiLabel + sep +
		t.Accent.Render(provider) + sep +
		profileLabel + sep +
		gitInfo + sep +
		status + sep +
		contextInfo
	if rb := m.renderReadBudgetIndicator(); rb != "" {
		bar += sep + rb
	}
	bar += sep + t.Muted.Render(cwd)
	return t.StatusBar.Render(bar)
}

// renderReadBudgetIndicator returns the "reads: N/M" status-bar chip when the
// guard is active and the model has burned at least half of the budget for
// the current turn. Returns "" so the chip is hidden when there's nothing
// useful to show — under 50% just adds noise. In explore mode (Threshold=0)
// we render a plain "reads: N" so the user sees the investigation depth
// without an enforced cap.
func (m model) renderReadBudgetIndicator() string {
	t := m.theme
	rb := m.readBudgetState
	if rb == nil || rb.Consumed <= 0 {
		return ""
	}
	if rb.Threshold <= 0 {
		// Explore / disabled — show count only, in muted styling.
		return t.Muted.Render(fmt.Sprintf("reads:%d", rb.Consumed))
	}
	label := fmt.Sprintf("reads:%d/%d", rb.Consumed, rb.Threshold)
	switch {
	case rb.Consumed >= rb.Threshold:
		// At or past the threshold — soft nudge fired (or about to). Use
		// the warning style; ErrorStyle would be too alarming since the
		// turn is still alive.
		return t.Warning.Render(label)
	case rb.Consumed*5 >= rb.Threshold*4:
		// >= 80% — caution.
		return t.Warning.Render(label)
	case rb.Consumed*2 >= rb.Threshold:
		// >= 50% — show but muted.
		return t.Muted.Render(label)
	default:
		return ""
	}
}

func (m model) activeModelRole() string {
	if !m.options.Config.ModelLoading.Enabled {
		return "chat"
	}
	switch m.agentRuntime.Mode {
	case "plan":
		return "planner"
	case "build":
		return "editor"
	case "explore":
		return "explorer"
	default:
		return "chat"
	}
}

func (m model) activeRoleModelID() string {
	role := m.activeModelRole()
	if model := strings.TrimSpace(m.options.Config.Models[role]); model != "" {
		return model
	}
	if role == "explorer" {
		if model := strings.TrimSpace(m.options.Config.Models["planner"]); model != "" {
			return model
		}
	}
	return strings.TrimSpace(m.options.Config.Models["chat"])
}

func (m model) activeRoleConfig() config.Config {
	role := m.activeModelRole()
	return config.ConfigForModelRole(m.options.Config, role, m.activeRoleModelID())
}

// refreshProviderState wipes the runtime's "currently loaded" tracking and
// kicks off a 5s background ProbeModel against the configured provider for
// the active role's model. Convenience wrapper around
// refreshProviderStateForRoles for callers that only need the active role.
func (m *model) refreshProviderState() {
	if m == nil || m.agentRuntime == nil {
		return
	}
	role := m.agentRuntime.ModelRoleForActiveMode()
	modelID := strings.TrimSpace(m.options.Config.Models[role])
	if modelID == "" {
		modelID = strings.TrimSpace(m.options.Config.Models["chat"])
	}
	if modelID == "" {
		return
	}
	m.refreshProviderStateForRoles(map[string]string{role: modelID})
}

// refreshProviderStateForRoles re-classifies the backend, then probes each
// unique modelID across the given role→modelID mapping and writes
// DetectedContext for every role using a successfully-probed model. Used by
// /provider and /model where the loaded-models cache must be wiped because
// the underlying provider or chat model just changed.
func (m *model) refreshProviderStateForRoles(roleModels map[string]string) {
	if m == nil || m.agentRuntime == nil {
		return
	}
	m.agentRuntime.ResetLoadedModels()
	m.probeProviderStateForRoles(roleModels)
}

// probeProviderStateForRoles probes each unique modelID and writes
// DetectedContext + LastProviderUsed without touching the loaded-models
// cache. Used by /model-multi after its own SetRoleModel / MarkModelLoaded
// loop has already populated the cache for the new selections — calling
// the wrapper would wipe those marks and force a redundant LoadModel on
// the next turn.
//
// Backend classification + LastProviderUsed update fires regardless of
// whether the probes return ctx info, so switching providers always
// repaints the status bar's backend label.
func (m *model) probeProviderStateForRoles(roleModels map[string]string) {
	if m == nil || m.agentRuntime == nil || m.options.Providers == nil {
		return
	}
	rt := m.agentRuntime
	cfg := m.options.Config
	providers := m.options.Providers
	// Snapshot the role-models so the goroutine isn't reading a map the
	// caller may keep mutating after we return.
	pairs := make([][2]string, 0, len(roleModels))
	for role, modelID := range roleModels {
		modelID = strings.TrimSpace(modelID)
		if role == "" || modelID == "" {
			continue
		}
		pairs = append(pairs, [2]string{role, modelID})
	}
	go func() {
		name := strings.TrimSpace(cfg.Providers.Default.Name)
		if name == "" {
			return
		}
		provider, ok := providers.Get(name)
		if !ok {
			return
		}
		// Force backend re-classification. The runtime's cached
		// BackendKind survives across config swaps when the same
		// provider instance stays in the registry, so explicit refresh
		// is the only way to recover from a hot-swap of the underlying
		// server.
		if refresher, ok := provider.(llm.BackendRefresher); ok {
			rctx, rcancel := context.WithTimeout(context.Background(), 5*time.Second)
			refresher.RefreshBackend(rctx)
			rcancel()
		}
		// Probe each unique modelID once; reuse the result across roles
		// that share the model.
		probed := map[string]*llm.ModelInfo{}
		for _, pair := range pairs {
			role, modelID := pair[0], pair[1]
			info, cached := probed[modelID]
			if !cached {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				probeInfo, err := provider.ProbeModel(ctx, modelID)
				cancel()
				if err == nil && probeInfo != nil {
					info = probeInfo
				}
				probed[modelID] = info
			}
			if info != nil && info.LoadedContextLength > 0 {
				rt.SetDetectedContext(role, &config.DetectedContext{
					ModelID:             info.ID,
					LoadedContextLength: info.LoadedContextLength,
					MaxContextLength:    info.MaxContextLength,
					ProbedAt:            time.Now().UTC(),
				})
			}
		}
		if bn, ok := provider.(llm.BackendNamer); ok {
			rt.SetLastProviderUsed(bn.BackendName())
		} else {
			rt.SetLastProviderUsed(provider.Name())
		}
	}()
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
		m.history = append(m.history, "    "+t.Muted.Render("-> ")+line)
		m.pendingAskUser = nil
		return nil
	}
	if m.agentRunning {
		m.history = append(m.history, t.Warning.Render("  Agent is still running."))
		return nil
	}
	if m.agentRuntime != nil && m.agentRuntime.Mode == "build" && m.completedChecklistFollowupShouldRefine(line) {
		_ = m.agentRuntime.SetMode("plan")
		m.showPlan = true
		m.recalcLayout()
		m.history = append(m.history, t.Muted.Render("Build completed; switching back to Plan mode to refine the finished work."))
		m.agentEvents = m.agentRuntime.Run(context.Background(), planRefinementPrompt(line))
		m.agentRunning = true
		return waitForAgentEvent(m.agentEvents)
	}
	// If in plan mode with tasks and the user wants to execute, route the
	// message straight through — the planner will now dispatch each task to
	// the builder subagent via execute_task, so no mode switch is needed.
	m.history = append(m.history, t.SeparatorLine(m.width-4))
	m.history = append(m.history, t.IndicatorAgent.Render("* ")+t.AgentPrefix.Render("forge"))
	m.history = append(m.history, "")
	m.modelProgress = nil
	m.readBudgetState = nil
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
		if hasPlan && looksLikeExecute(line) {
			return m.runPlanExecution("Execute the approved plan.")
		}
		if hasPlan && looksLikePlanReset(line) {
			m.pendingPlanLine = line
			m.activeForm = formConfirmPlanReset
			m.confirmPlanReset = newConfirmFormWithDefault("A prior plan exists. Clear it and start fresh?", m.theme, false)
			return nil
		}
		if hasPlan {
			m.agentEvents = m.agentRuntime.Run(context.Background(), planRefinementPrompt(line))
			m.agentRunning = true
			return waitForAgentEvent(m.agentEvents)
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
	case "/init":
		return m.handleInitCommand(fields)
	case "/quit", "/exit":
		m.quitting = true
		return m.theme.Muted.Render("Goodbye.")
	case "/update":
		return m.handleUpdateCommand()
	case "/refresh-config":
		return m.handleRefreshConfigCommand()
	case "/reads":
		return m.handleReadsCommand(fields)
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
			return m.agentUsageHint()
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
	case "/permissions", "/profile":
		// Both shapes accepted:
		//   /permissions set <name>   /profile set <name>
		//   /permissions <name>       /profile <name>
		// The bare `/profile <name>` form skips the "set" verb because the
		// /profile alias is meant to be the fast path. With no args the
		// command opens the interactive picker.
		if len(fields) >= 3 && fields[1] == "set" {
			return m.setPermissionProfile(fields[2])
		}
		if len(fields) >= 2 && fields[1] != "set" {
			return m.setPermissionProfile(fields[1])
		}
		m.activeForm = formProfile
		m.profileForm = newProfileForm(commandProfileName(m.agentRuntime.Commands), m.theme)
		return "Opening permission profile selector..."
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
		if m.options.MCP == nil {
			return "MCP not loaded."
		}
		if len(fields) > 1 {
			switch fields[1] {
			case "resources":
				return m.describeMCPResources()
			case "prompts":
				return m.describeMCPPrompts()
			}
		}
		return m.options.MCP.Describe()
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
	case "/claw":
		return m.handleClawCommand(fields)
	case "/remote-control", "/remote":
		return m.handleRemoteCommand(fields)
	case "/code":
		return m.openInVSCode()
	default:
		// Plugin commands: /<plugin>:<command>. Match before falling
		// through to "Unknown command" so a Claude Code plugin's
		// commands/ files become first-class TUI commands without each
		// plugin needing to register code.
		if msg, ok := m.dispatchPluginCommand(fields[0], fields[1:]); ok {
			return msg
		}
		// Skill commands: /<skill-name> resolves against installed skills
		// (same dirs run_skill scans). Built-ins above always win on a
		// name collision, so this only fires for unmatched names.
		if msg, ok := m.dispatchSkillCommand(fields[0], fields[1:]); ok {
			return msg
		}
		return "Unknown command. Try /help."
	}
}

// dispatchPluginCommand matches /<plugin>:<command> against the discovered
// plugins' commands/ directories. On a hit, the markdown content is sent to
// the runtime as a user message — same path as if the user had typed the
// instructions themselves. Extra args are appended verbatim so a command
// like /sample-plugin:hello world expands to <command body>\n\nworld.
//
// Returns (statusMessage, true) on dispatch and ("", false) when the input
// is not a plugin command.
func (m *model) dispatchPluginCommand(head string, rest []string) (string, bool) {
	if !strings.HasPrefix(head, "/") {
		return "", false
	}
	if !strings.Contains(head, ":") {
		return "", false
	}
	if m.options.Plugins == nil {
		return "", false
	}
	parts := strings.SplitN(strings.TrimPrefix(head, "/"), ":", 2)
	pluginName, cmdName := parts[0], parts[1]
	if pluginName == "" || cmdName == "" {
		return "", false
	}
	discovered, err := m.options.Plugins.Discover()
	if err != nil {
		return m.theme.ErrorStyle.Render("plugin discovery failed: " + err.Error()), true
	}
	for _, p := range discovered {
		if p.Name != pluginName {
			continue
		}
		for _, c := range plugins.LoadCommands(p.Path) {
			if c.Name != cmdName {
				continue
			}
			body := strings.TrimSpace(c.Content)
			if extra := strings.TrimSpace(strings.Join(rest, " ")); extra != "" {
				body += "\n\n" + extra
			}
			m.agentEvents = m.agentRuntime.Run(context.Background(), body)
			m.agentRunning = true
			m.pendingCommand = waitForAgentEvent(m.agentEvents)
			return "Running " + head + " from " + pluginName, true
		}
	}
	return m.theme.Warning.Render(head + ": plugin or command not found"), true
}

// dispatchSkillCommand matches /<skill-name> against installed skills
// (workspace + home dirs the run_skill tool scans). On a hit, the
// SKILL.md body (frontmatter stripped) is sent to the runtime as a user
// message — same path as dispatchPluginCommand. Extra args are appended
// as "User context: ...".
//
// Frontmatter directives (tools/script/models) are NOT honored on this
// dispatch path; they only apply when the LLM invokes run_skill
// explicitly. The skill body can instruct the model to call run_skill
// itself if it needs the script output.
//
// Returns ("", false) when the name is not an installed skill so the
// caller can fall through to "Unknown command".
func (m *model) dispatchSkillCommand(head string, rest []string) (string, bool) {
	if !strings.HasPrefix(head, "/") || strings.Contains(head, ":") {
		return "", false
	}
	if m.options.Skills == nil {
		return "", false
	}
	name := strings.TrimPrefix(head, "/")
	if name == "" {
		return "", false
	}
	detail, err := m.options.Skills.LoadSkill(name)
	if err != nil {
		return "", false
	}
	data, err := os.ReadFile(detail.Path)
	if err != nil {
		return m.theme.ErrorStyle.Render("read skill " + name + ": " + err.Error()), true
	}
	body := strings.TrimSpace(stripSkillFrontmatter(string(data)))
	if extra := strings.TrimSpace(strings.Join(rest, " ")); extra != "" {
		body += "\n\nUser context: " + extra
	}
	m.agentEvents = m.agentRuntime.Run(context.Background(), body)
	m.agentRunning = true
	m.pendingCommand = waitForAgentEvent(m.agentEvents)
	return "Running " + head + " from skills", true
}

// stripSkillFrontmatter removes a leading YAML frontmatter block
// (delimited by `---` lines) so the body sent to the runtime doesn't
// carry the metadata header. If no frontmatter is present, the input is
// returned unchanged.
func stripSkillFrontmatter(content string) string {
	if !strings.HasPrefix(content, "---\n") && !strings.HasPrefix(content, "---\r\n") {
		return content
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(content, "---\r\n"), "---\n")
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return content
	}
	after := rest[idx+len("\n---"):]
	after = strings.TrimPrefix(after, "\r")
	after = strings.TrimPrefix(after, "\n")
	return after
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
			if next != "plan" && next != "build" && m.showPlan {
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
	searchActive := m.searchMode.query != ""
	if searchActive {
		filtered, positions := FilterHistory(content, m.searchMode.query, m.searchMode.currentIdx)
		content = filtered
		if m.searchMode.currentIdx >= len(positions) {
			m.searchMode.currentIdx = 0
		}
		m.searchMode.positions = positions
	}
	// Reserved trailing padding so the last rendered line can never sit flush
	// against the bottom edge of the viewport and appear crowded by the input.
	var joined string
	if !searchActive {
		fp := historyFingerprint(content)
		if fp == m.fullRenderFingerprint && m.fullRenderCache != "" {
			joined = m.fullRenderCache
		} else {
			joined = strings.Join(content, "\n")
			m.fullRenderCache = joined
			m.fullRenderFingerprint = fp
		}
	} else {
		joined = strings.Join(content, "\n")
		// Search results invalidate the full cache — next unfiltered refresh
		// will rebuild.
		m.fullRenderCache = ""
		m.fullRenderFingerprint = 0
	}
	rendered := joined + strings.Repeat("\n", viewportInputGapLines)
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

// flushStreaming rebuilds the streamed history line from streamingRaw,
// running the <think> filter controlled by m.thinkEnabled and indenting
// the result. Called at ~30fps from streamFlushMsg and synchronously before
// any non-delta event that mutates m.history, so downstream event handlers
// always observe up-to-date history. Safe to call when not streaming.
//
// The transform runs on every flush (not per-delta) so toggling Ctrl+T
// re-renders the same raw bytes through the new filter immediately — no
// retroactive state to reconcile.
func (m *model) flushStreaming() {
	if !m.streaming || m.streamingStartIdx < 0 || m.streamingStartIdx >= len(m.history) {
		return
	}
	raw := m.streamingRaw.String()
	lastAgentResponse = raw
	rendered := formatStreamingText(raw, m.thinkEnabled, m.theme)
	m.history[m.streamingStartIdx] = indentBlock(rendered, "    ")
}

// indentBlock prepends prefix to every line of s. Splits on "\n" and rejoins
// rather than a ReplaceAll so ANSI escape sequences that happen to straddle
// newlines aren't corrupted by mid-sequence whitespace insertion.
func indentBlock(s, prefix string) string {
	if s == "" {
		return prefix
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
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

func looksLikePlanReset(line string) bool {
	lower := strings.TrimSpace(strings.ToLower(line))
	keywords := []string{
		"new plan", "start over", "start from scratch", "from scratch", "restart the plan",
		"replan", "plan from scratch", "fresh plan",
		"plan nuevo", "nuevo plan", "empecemos de cero", "empezar de cero", "desde cero",
		"reinicia el plan", "rehaz el plan", "haz un plan nuevo",
	}
	for _, kw := range keywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

func looksLikeBuildFollowupRefinement(line string) bool {
	lower := strings.TrimSpace(strings.ToLower(line))
	if lower == "" {
		return false
	}
	for _, kw := range []string{
		"change", "modify", "update", "adjust", "tweak", "revise", "refine",
		"add", "remove", "fix", "improve", "follow-up",
		"cambia", "modifica", "actualiza", "ajusta", "retoca", "revisa",
		"agrega", "añade", "anade", "quita", "corrige", "mejora", "modificaciones",
	} {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

func (m *model) completedChecklistFollowupShouldRefine(line string) bool {
	if m == nil || m.agentRuntime == nil || m.agentRuntime.Mode != "build" {
		return false
	}
	if !looksLikeBuildFollowupRefinement(line) {
		return false
	}
	if m.agentRuntime.Tasks == nil {
		return false
	}
	list, err := m.agentRuntime.Tasks.List()
	if err != nil || len(list) == 0 {
		return false
	}
	hasCompleted := false
	for _, task := range list {
		switch task.Status {
		case "pending", "in_progress":
			return false
		case "completed", "done":
			hasCompleted = true
		}
	}
	return hasCompleted
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
	// gitBannerView is rendered between the viewport and the input
	// (see View() near the chatArea/inputArea concatenation). When the
	// worktree goes dirty the banner adds 2 visible rows; without
	// counting them here the viewport height comes out 2 too tall and
	// the last rows of chat content overlap the input — the user sees
	// the latest reply hidden underneath the textarea until something
	// triggers another recalcLayout (Esc, new stream, resize). Empty
	// banner returns "" so we guard against the lipgloss empty-string
	// quirk that would otherwise add 1 phantom line.
	if banner := m.gitBannerView(); banner != "" {
		height += lipgloss.Height(banner)
	}
	if len(m.suggestions) > 0 {
		height += 1 + lipgloss.Height(m.suggestionView())
	}
	return height + 2 + statusHeight
}

func (m model) activeFormView() string {
	switch m.activeForm {
	case formAskUser:
		return m.askUserForm.View()
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
	case formConfirmPlanReset:
		return m.confirmPlanReset.View()
	case formConfirmExplorerPlan:
		return m.confirmExplorerPlan.View()
	case formYarnSettings:
		return m.yarnSettingsForm.View()
	case formYarnMenu:
		return m.yarnMenuForm.View()
	case formProfile:
		return m.profileForm.View()
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

// historyFingerprint produces a cheap 64-bit key for a history slice. FNV-1a
// over length plus the length and first 16 bytes of the last 8 lines. Two
// different histories can collide, but in practice the combination of line
// count and tail content is stable enough to catch layout-only refreshes
// without re-joining.
func historyFingerprint(history []string) uint64 {
	const offset64 uint64 = 14695981039346656037
	const prime64 uint64 = 1099511628211
	h := offset64
	h ^= uint64(len(history))
	h *= prime64
	start := len(history) - 8
	if start < 0 {
		start = 0
	}
	for i := start; i < len(history); i++ {
		line := history[i]
		h ^= uint64(len(line))
		h *= prime64
		limit := len(line)
		if limit > 16 {
			limit = 16
		}
		for j := 0; j < limit; j++ {
			h ^= uint64(line[j])
			h *= prime64
		}
	}
	return h
}
