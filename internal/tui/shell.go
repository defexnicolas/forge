package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"forge/internal/config"
	"forge/internal/globalconfig"
	"forge/internal/llm"
	"forge/internal/session"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type WorkspaceSession struct {
	Options   Options
	CloseFunc func() error
}

func (s *WorkspaceSession) Close() error {
	if s == nil || s.CloseFunc == nil {
		return nil
	}
	return s.CloseFunc()
}

type OpenWorkspaceFunc func(cwd, resume string) (*WorkspaceSession, error)

type ShellOptions struct {
	InitialWorkspace *WorkspaceSession
	InitialHubDir    string
	OpenWorkspace    OpenWorkspaceFunc
	StateStore       HubStateStore
}

type ShellApp struct {
	options ShellOptions
}

func NewShell(options ShellOptions) *ShellApp {
	return &ShellApp{options: options}
}

func (a *ShellApp) Run(ctx context.Context) error {
	program := tea.NewProgram(newShellModel(a.options), tea.WithContext(ctx))
	finalModel, err := program.Run()
	switch final := finalModel.(type) {
	case shellModel:
		closeErr := (&final).Close()
		if err == nil {
			err = closeErr
		}
	case *shellModel:
		closeErr := final.Close()
		if err == nil {
			err = closeErr
		}
	}
	return err
}

type appMode string
type appPane string
type appView string

const (
	modeHub       appMode = "hub"
	modeWorkspace appMode = "workspace"

	paneSidebar appPane = "sidebar"
	paneMain    appPane = "main"
	paneInput   appPane = "input"

	viewExplorer  appView = "explorer"
	viewRecent    appView = "recent"
	viewPinned    appView = "pinned"
	viewSessions  appView = "sessions"
	viewTools     appView = "tools"
	viewMCPs      appView = "mcps"
	viewSettings  appView = "settings"
	viewChat      appView = "chat"
	viewPlan      appView = "plan"
	viewDiff      appView = "diff"
	viewHub       appView = "hub"
	viewMigration appView = "migration"
)

const shellSidebarWidth = 24

type shellSidebarItem struct {
	View  appView
	Label string
	Hint  string
}

type explorerEntry struct {
	Name  string
	Path  string
	IsDir bool
}

type shellModel struct {
	options            ShellOptions
	theme              Theme
	width              int
	height             int
	mode               appMode
	activePane         appPane
	activeView         appView
	sidebarIndex       int
	hubState           HubState
	explorerDir        string
	explorerEntries    []explorerEntry
	explorerIndex      int
	recentIndex        int
	pinnedIndex        int
	hubChat            *model
	hubChatSession     *WorkspaceSession
	workspace          *model
	workspaceSession   *WorkspaceSession
	activeHubForm      hubFormMode
	providerForm       providerForm
	modelForm          modelForm
	modelMultiForm     modelMultiForm
	yarnSettingsForm   yarnSettingsForm
	themeForm          themeForm
	skillsForm         skillsForm
	hubSettingsIndex   int
	migrationProposals []migrationProposal
	statusMessage      string
	lastEscTime        time.Time
}

func newShellModel(options ShellOptions) shellModel {
	theme := DefaultTheme()
	hubState := HubState{}
	if options.StateStore != nil {
		if loaded, err := options.StateStore.Load(); err == nil {
			hubState = loaded
		}
	}
	dir := strings.TrimSpace(options.InitialHubDir)
	if dir == "" {
		dir = hubState.LastHubDir
	}
	if dir == "" {
		if wd, err := os.Getwd(); err == nil {
			dir = wd
		}
	}
	if dir == "" {
		dir = "."
	}
	dir = normalizeDir(dir)

	m := shellModel{
		options:       options,
		theme:         theme,
		width:         100,
		height:        32,
		mode:          modeHub,
		activePane:    paneMain,
		activeView:    viewExplorer,
		explorerDir:   dir,
		explorerIndex: 0,
	}
	m.loadExplorerDir(dir)
	if options.InitialWorkspace != nil {
		m.attachWorkspace(options.InitialWorkspace)
	}
	// Apply persisted theme from the global config so the Hub paints with
	// the user's choice from the very first frame.
	if g, err := globalconfig.Load(); err == nil && g.Theme != nil && *g.Theme != "" {
		m.theme = GetTheme(*g.Theme)
	}
	// First-run migration check: if the user has Recent / Pinned workspaces
	// with theme/models/yarn we now serve from the global config, surface
	// the wizard before they touch anything else.
	if !hubState.MigrationDone && options.InitialWorkspace == nil {
		props := scanWorkspacesForMigration(hubState)
		if len(props) > 0 {
			m.migrationProposals = props
			m.activeView = viewMigration
		} else {
			// Nothing to migrate; flip the flag so we don't keep scanning.
			m.hubState.MigrationDone = true
			m.saveHubState()
		}
	}
	return m
}

func (m shellModel) Init() tea.Cmd {
	if m.workspace != nil {
		return m.runWorkspaceInit()
	}
	return nil
}

func (m shellModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if result, cmd, handled := m.handleHubFormUpdate(msg); handled {
		return result, cmd
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, tea.Batch(m.resizeWorkspace(), m.resizeHubChat())
	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlC {
			return m, tea.Quit
		}
		if m.mode == modeHub && m.activeView == viewChat && m.hubChat != nil && m.activePane == paneInput && !m.hubChatHasModal() {
			if msg.String() == "ctrl+w" || msg.Type == tea.KeyF6 {
				m.rotatePane(1)
				return m, m.resizeHubChat()
			}
			return m, m.forwardHubChat(msg)
		}
		if m.mode == modeWorkspace && m.activePane == paneInput && !m.workspaceHasModal() {
			if msg.String() == "ctrl+w" || msg.Type == tea.KeyF6 {
				m.rotatePane(1)
				return m, m.resizeWorkspace()
			}
			return m, m.forwardWorkspace(msg)
		}
		if m.hubChatHasModal() {
			return m, m.forwardHubChat(msg)
		}
		if m.workspaceHasModal() {
			return m, m.forwardWorkspace(msg)
		}
		return m.handleKey(msg)
	default:
		if m.mode == modeHub && m.activeView == viewChat && m.hubChat != nil {
			return m, m.forwardHubChat(msg)
		}
		if m.workspace != nil {
			return m, m.forwardWorkspace(msg)
		}
	}
	return m, nil
}

func (m shellModel) View() string {
	if m.mode == modeHub && m.activeView == viewChat && m.hubChatHasModal() {
		return m.hubChat.View()
	}
	if m.workspace != nil && m.workspaceHasModal() {
		return m.workspace.View()
	}
	sidebar := m.sidebarView()
	if m.mode == modeHub && m.activeView == viewChat {
		if m.hubChat != nil {
			return lipgloss.JoinHorizontal(lipgloss.Top, sidebar, m.hubChat.View())
		}
		return lipgloss.JoinHorizontal(lipgloss.Top, sidebar, m.hubChatEmptyView())
	}
	if m.mode == modeWorkspace && m.workspace != nil {
		return lipgloss.JoinHorizontal(lipgloss.Top, sidebar, m.workspace.View())
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, sidebar, m.hubView())
}

func (m *shellModel) Close() error {
	if err := m.closeHubChat(); err != nil {
		return err
	}
	return m.closeWorkspace()
}

func (m *shellModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyTab:
		if m.mode == modeHub {
			m.rotatePane(1)
			return *m, nil
		}
		return *m, nil
	case tea.KeyShiftTab:
		if m.mode == modeHub {
			m.rotatePane(-1)
			return *m, nil
		}
		return *m, nil
	case tea.KeyRunes:
		if msg.String() == "ctrl+w" {
			m.rotatePane(1)
			return *m, m.resizeWorkspace()
		}
		if m.mode == modeHub && (m.activePane == paneMain || m.activePane == paneSidebar) && len(msg.Runes) == 1 {
			switch strings.ToLower(string(msg.Runes[0])) {
			case "o":
				m.openSelectedWorkspace()
				return *m, m.resizeWorkspace()
			case "p":
				m.togglePinForActiveSelection()
				return *m, nil
			}
		}
	case tea.KeyF6:
		m.rotatePane(1)
		return *m, m.resizeWorkspace()
	case tea.KeyEsc:
		return m.handleEsc()
	case tea.KeyUp:
		return m.handleUp()
	case tea.KeyDown:
		return m.handleDown()
	case tea.KeyEnter:
		return m.handleEnter()
	case tea.KeyBackspace, tea.KeyLeft:
		if m.mode == modeHub && m.activePane == paneMain && m.activeView == viewExplorer {
			m.moveExplorerParent()
			return *m, nil
		}
	}
	if msg.String() == "ctrl+w" {
		m.rotatePane(1)
		return *m, m.resizeWorkspace()
	}
	if m.mode == modeHub && msg.Type == tea.KeyRunes && len(msg.Runes) == 1 {
		switch strings.ToLower(string(msg.Runes[0])) {
		case "h":
			m.moveExplorerParent()
			return *m, nil
		}
	}
	if m.workspace != nil {
		return *m, m.forwardWorkspace(msg)
	}
	if m.mode == modeHub && m.activeView == viewChat && m.hubChat != nil {
		return *m, m.forwardHubChat(msg)
	}
	return *m, nil
}

func (m *shellModel) handleEsc() (tea.Model, tea.Cmd) {
	now := time.Now()
	if m.mode == modeWorkspace {
		if m.activeView != viewChat {
			m.activeView = viewChat
			return *m, nil
		}
		if m.activePane != paneSidebar {
			m.activePane = paneSidebar
			return *m, m.resizeWorkspace()
		}
		if err := m.SwitchToHub(); err != nil {
			m.statusMessage = "Close workspace failed: " + err.Error()
		}
		return *m, nil
	}
	if m.activeView == viewMigration {
		// Esc dismisses the wizard without applying. Mark Migration done
		// regardless so the wizard does not re-trigger on the next launch.
		m.dismissMigration()
		return *m, nil
	}
	if m.activeView != viewExplorer {
		m.selectSidebarView(viewExplorer)
		return *m, nil
	}
	if now.Sub(m.lastEscTime) < 500*time.Millisecond {
		return *m, tea.Quit
	}
	m.lastEscTime = now
	m.statusMessage = "Press Esc again to quit."
	return *m, nil
}

func (m *shellModel) handleUp() (tea.Model, tea.Cmd) {
	switch m.activePane {
	case paneSidebar:
		if m.sidebarIndex > 0 {
			m.selectSidebarView(m.currentSidebarItems()[m.sidebarIndex-1].View)
		}
	case paneMain:
		if m.mode == modeHub {
			switch m.activeView {
			case viewExplorer:
				if m.explorerIndex > 0 {
					m.explorerIndex--
				}
			case viewRecent:
				if m.recentIndex > 0 {
					m.recentIndex--
				}
			case viewPinned:
				if m.pinnedIndex > 0 {
					m.pinnedIndex--
				}
			case viewSettings:
				if m.hubSettingsIndex > 0 {
					m.hubSettingsIndex--
				}
			}
			return *m, nil
		}
	}
	if m.workspace != nil {
		return *m, m.forwardWorkspace(tea.KeyMsg{Type: tea.KeyUp})
	}
	return *m, nil
}

func (m *shellModel) handleDown() (tea.Model, tea.Cmd) {
	switch m.activePane {
	case paneSidebar:
		items := m.currentSidebarItems()
		if m.sidebarIndex < len(items)-1 {
			m.selectSidebarView(items[m.sidebarIndex+1].View)
		}
	case paneMain:
		if m.mode == modeHub {
			switch m.activeView {
			case viewExplorer:
				if m.explorerIndex < len(m.explorerEntries)-1 {
					m.explorerIndex++
				}
			case viewRecent:
				if m.recentIndex < len(m.hubState.RecentWorkspaces)-1 {
					m.recentIndex++
				}
			case viewPinned:
				if m.pinnedIndex < len(m.hubState.Pinned)-1 {
					m.pinnedIndex++
				}
			case viewSettings:
				if m.hubSettingsIndex < len(m.hubSettingsItems())-1 {
					m.hubSettingsIndex++
				}
			}
			return *m, nil
		}
	}
	if m.workspace != nil {
		return *m, m.forwardWorkspace(tea.KeyMsg{Type: tea.KeyDown})
	}
	return *m, nil
}

func (m *shellModel) handleEnter() (tea.Model, tea.Cmd) {
	if m.activePane == paneSidebar {
		if m.mode == modeWorkspace {
			return *m, m.activateWorkspaceSidebar()
		}
		m.selectSidebarView(m.currentSidebarItems()[m.sidebarIndex].View)
		if m.activeView == viewChat {
			m.activePane = paneInput
			return *m, m.ensureHubChatSession()
		}
		m.activePane = paneMain
		return *m, nil
	}
	if m.mode == modeHub && m.activePane == paneMain {
		switch m.activeView {
		case viewMigration:
			m.acceptMigration()
			return *m, nil
		case viewExplorer:
			m.enterExplorerSelection()
			return *m, nil
		case viewRecent:
			m.openRecentWorkspace()
			return *m, m.resizeWorkspace()
		case viewPinned:
			m.openPinnedWorkspace()
			return *m, m.resizeWorkspace()
		case viewSettings:
			items := m.hubSettingsItems()
			if m.hubSettingsIndex >= 0 && m.hubSettingsIndex < len(items) {
				items[m.hubSettingsIndex].Open(m)
			}
			return *m, nil
		case viewChat:
			if m.hubChat == nil {
				m.activePane = paneInput
				return *m, m.ensureHubChatSession()
			}
			m.activePane = paneInput
			return *m, m.resizeHubChat()
		}
	}
	if m.mode == modeWorkspace && m.activePane == paneMain && m.activeView == viewChat {
		m.activePane = paneInput
		return *m, m.resizeWorkspace()
	}
	return *m, nil
}

func (m *shellModel) rotatePane(step int) {
	panes := []appPane{paneSidebar, paneMain}
	if m.mode == modeWorkspace || (m.mode == modeHub && m.activeView == viewChat) {
		panes = append(panes, paneInput)
	}
	index := 0
	for i, pane := range panes {
		if pane == m.activePane {
			index = i
			break
		}
	}
	index = (index + step + len(panes)) % len(panes)
	m.activePane = panes[index]
}

func (m *shellModel) attachWorkspace(session *WorkspaceSession) {
	if session == nil {
		return
	}
	workspace := newModel(session.Options)
	m.workspaceSession = session
	m.workspace = &workspace
	m.mode = modeWorkspace
	m.activeView = viewChat
	m.activePane = paneInput
	m.sidebarIndex = 0
	m.statusMessage = ""
	m.recordRecentWorkspace(session.Options.CWD)
	m.syncWorkspaceFocus()
}

func (m *shellModel) OpenWorkspace(cwd string) error {
	if m.options.OpenWorkspace == nil {
		return fmt.Errorf("workspace opener unavailable")
	}
	session, err := m.options.OpenWorkspace(cwd, "")
	if err != nil {
		return err
	}
	if err := m.closeWorkspace(); err != nil {
		_ = session.Close()
		return err
	}
	_ = m.closeHubChat()
	m.attachWorkspace(session)
	return nil
}

func (m *shellModel) CloseWorkspace() error {
	return m.closeWorkspace()
}

func (m *shellModel) SwitchToHub() error {
	if err := m.closeWorkspace(); err != nil {
		return err
	}
	m.mode = modeHub
	m.activePane = paneMain
	m.activeView = viewExplorer
	m.selectSidebarView(viewExplorer)
	return nil
}

func (m *shellModel) closeWorkspace() error {
	if m.workspace != nil {
		_ = m.workspace.close()
	}
	var err error
	if m.workspaceSession != nil {
		err = m.workspaceSession.Close()
	}
	m.workspace = nil
	m.workspaceSession = nil
	return err
}

func (m *shellModel) closeHubChat() error {
	if m.hubChat != nil {
		_ = m.hubChat.close()
	}
	var err error
	if m.hubChatSession != nil {
		err = m.hubChatSession.Close()
	}
	m.hubChat = nil
	m.hubChatSession = nil
	return err
}

func (m *shellModel) applyHubChatConfig(cfg config.Config) {
	if m.hubChat == nil {
		return
	}
	providers := hubSettingsProviders(cfg)
	m.hubChat.options.Config = cfg
	m.hubChat.options.Providers = providers
	if m.hubChat.agentRuntime == nil {
		return
	}
	m.hubChat.agentRuntime.Config = cfg
	m.hubChat.agentRuntime.Builder.Config = cfg
	m.hubChat.agentRuntime.Providers = providers
	for role, modelID := range cfg.Models {
		modelID = strings.TrimSpace(modelID)
		if modelID == "" {
			continue
		}
		m.hubChat.agentRuntime.SetRoleModel(role, modelID)
	}
	m.hubChat.agentRuntime.SetChatModel(strings.TrimSpace(cfg.Models["chat"]))
}

func (m *shellModel) hubChatHasModal() bool {
	return m.hubChat != nil && (m.hubChat.activeForm != formNone || m.hubChat.searching)
}

func (m *shellModel) workspaceHasModal() bool {
	return m.workspace != nil && (m.workspace.activeForm != formNone || m.workspace.searching)
}

func (m *shellModel) runWorkspaceInit() tea.Cmd {
	if m.workspace == nil {
		return nil
	}
	m.syncWorkspaceFocus()
	return tea.Batch(m.resizeWorkspace(), m.workspace.Init())
}

func (m *shellModel) resizeWorkspace() tea.Cmd {
	if m.workspace == nil {
		return nil
	}
	m.syncWorkspaceFocus()
	updated, cmd := m.workspace.Update(tea.WindowSizeMsg{
		Width:  max(40, m.width-shellSidebarWidth-1),
		Height: max(10, m.height),
	})
	if workspace, ok := updated.(model); ok {
		m.workspace = &workspace
		m.syncWorkspaceFocus()
	}
	return cmd
}

func (m *shellModel) resizeHubChat() tea.Cmd {
	if m.hubChat == nil {
		return nil
	}
	m.syncHubChatFocus()
	updated, cmd := m.hubChat.Update(tea.WindowSizeMsg{
		Width:  max(40, m.width-shellSidebarWidth-1),
		Height: max(10, m.height),
	})
	if chat, ok := updated.(model); ok {
		m.hubChat = &chat
		m.syncHubChatFocus()
	}
	return cmd
}

func (m *shellModel) forwardWorkspace(msg tea.Msg) tea.Cmd {
	if m.workspace == nil {
		return nil
	}
	m.syncWorkspaceFocus()
	updated, cmd := m.workspace.Update(msg)
	if workspace, ok := updated.(model); ok {
		m.workspace = &workspace
		m.syncWorkspaceFocus()
	}
	return cmd
}

func (m *shellModel) forwardHubChat(msg tea.Msg) tea.Cmd {
	if m.hubChat == nil {
		return nil
	}
	m.syncHubChatFocus()
	updated, cmd := m.hubChat.Update(msg)
	if chat, ok := updated.(model); ok {
		m.hubChat = &chat
		m.syncHubChatFocus()
	}
	return cmd
}

func (m *shellModel) syncWorkspaceFocus() {
	if m.workspace == nil {
		return
	}
	if m.mode == modeWorkspace && m.activePane == paneInput {
		m.workspace.input.Focus()
		return
	}
	m.workspace.input.Blur()
}

func (m *shellModel) syncHubChatFocus() {
	if m.hubChat == nil {
		return
	}
	if m.mode == modeHub && m.activeView == viewChat && m.activePane == paneInput {
		m.hubChat.input.Focus()
		return
	}
	m.hubChat.input.Blur()
}

func (m shellModel) sidebarView() string {
	items := m.currentSidebarItems()
	lines := []string{
		m.theme.Accent.Render("  Forge"),
		m.theme.Muted.Render("  " + strings.ToUpper(string(m.mode))),
		"",
	}
	if m.mode == modeWorkspace && m.workspace != nil {
		lines = append(lines, m.theme.StatusValue.Render("  "+compactDisplayPath(m.workspace.options.CWD)), "")
	}
	for i, item := range items {
		label := "  " + item.Label
		if i == m.sidebarIndex {
			label = m.theme.StatusActive.Render("> " + item.Label)
		} else if item.View == m.activeView {
			label = m.theme.StatusValue.Render("* " + item.Label)
		}
		lines = append(lines, label)
		if i == m.sidebarIndex {
			lines = append(lines, m.theme.Muted.Render("    "+item.Hint))
		}
	}
	if m.mode == modeWorkspace {
		lines = append(lines, "", m.theme.Muted.Render("  Ctrl+W/F6 panes"), m.theme.Muted.Render("  Tab autocomplete"), m.theme.Muted.Render("  Esc -> Hub"))
	} else {
		lines = append(lines, "", m.theme.Muted.Render("  Tab sidebar/main"), m.theme.Muted.Render("  Enter activate"), m.theme.Muted.Render("  O open workspace"))
	}
	style := lipgloss.NewStyle().
		Width(shellSidebarWidth).
		Height(max(1, m.height-4)).
		Padding(1, 1).
		BorderStyle(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.Color("240"))
	if m.activePane == paneSidebar {
		style = style.BorderForeground(lipgloss.Color("78"))
	}
	return style.Render(strings.Join(lines, "\n"))
}

func (m shellModel) currentSidebarItems() []shellSidebarItem {
	if m.mode == modeWorkspace {
		return []shellSidebarItem{
			{View: viewChat, Label: "Chat", Hint: "conversation"},
			{View: viewPlan, Label: "Plan", Hint: "plan panel"},
			{View: viewDiff, Label: "Diff", Hint: "git diff"},
			{View: viewSessions, Label: "Sessions", Hint: "workspace logs"},
			{View: viewTools, Label: "Tools", Hint: "registered tools"},
			{View: viewMCPs, Label: "MCPs", Hint: "connected servers"},
			{View: viewSettings, Label: "Settings", Hint: "workspace config"},
			{View: viewHub, Label: "Hub", Hint: "close workspace"},
		}
	}
	return []shellSidebarItem{
		{View: viewExplorer, Label: "Explorer", Hint: "browse folders"},
		{View: viewPinned, Label: "Pinned", Hint: "favorite workspaces"},
		{View: viewRecent, Label: "Recent", Hint: "reopen quickly"},
		{View: viewSessions, Label: "Sessions", Hint: "workspace session logs"},
		{View: viewTools, Label: "Tools", Hint: "global info"},
		{View: viewMCPs, Label: "MCPs", Hint: "global info"},
		{View: viewSettings, Label: "Settings", Hint: "hub state"},
		{View: viewChat, Label: "Chat", Hint: "general chat"},
	}
}

func (m *shellModel) selectSidebarView(view appView) {
	items := m.currentSidebarItems()
	for i, item := range items {
		if item.View == view {
			if m.mode == modeHub && m.activeView != view {
				m.statusMessage = ""
			}
			m.sidebarIndex = i
			m.activeView = view
			return
		}
	}
}

func (m *shellModel) activateWorkspaceSidebar() tea.Cmd {
	switch m.currentSidebarItems()[m.sidebarIndex].View {
	case viewChat:
		m.activeView = viewChat
		m.activePane = paneInput
		m.workspace.showPlan = false
		m.workspace.recalcLayout()
		m.workspace.refresh()
	case viewPlan:
		m.activeView = viewPlan
		m.workspace.showPlan = true
		m.workspace.recalcLayout()
		m.workspace.refresh()
	case viewDiff:
		m.activeView = viewDiff
		m.pushWorkspacePanelOutput("diff", m.workspace.describeDiff())
	case viewSessions:
		m.activeView = viewSessions
		m.pushWorkspacePanelOutput("sessions", m.workspace.describeSessions())
	case viewTools:
		m.activeView = viewTools
		m.pushWorkspacePanelOutput("tools", m.workspace.describeTools())
	case viewMCPs:
		m.activeView = viewMCPs
		if m.workspace.options.MCP != nil {
			m.pushWorkspacePanelOutput("mcp", m.workspace.options.MCP.Describe())
		}
	case viewSettings:
		m.activeView = viewSettings
		m.pushWorkspacePanelOutput("settings", m.workspace.describeWorkspaceSettings())
	case viewHub:
		if err := m.SwitchToHub(); err != nil {
			m.statusMessage = "Close workspace failed: " + err.Error()
		}
	}
	return m.resizeWorkspace()
}

func (m *shellModel) pushWorkspacePanelOutput(label, body string) {
	if m.workspace == nil || strings.TrimSpace(body) == "" {
		return
	}
	header := m.workspace.theme.IndicatorAgent.Render("* ") + m.workspace.theme.AgentPrefix.Render("forge /"+label)
	m.workspace.history = append(m.workspace.history, "", header, "", indentBlock(body, "    "))
	m.workspace.forceScrollBottom = true
	m.workspace.refresh()
}

func (m shellModel) hubChatEmptyView() string {
	style := lipgloss.NewStyle().
		Width(m.hubContentWidth()).
		Height(m.hubInnerHeight()).
		Padding(1, 1)
	lines := []string{
		m.theme.Accent.Render("  Hub Chat"),
		m.theme.Muted.Render("  Quick chat without leaving the Hub."),
		"",
		m.theme.Muted.Render("This chat is global and not tied to Explorer, Pinned, or Recent."),
		"",
		m.theme.Muted.Render("Press Enter to open the Hub chat."),
	}
	return style.Render(strings.Join(lines, "\n"))
}

func (m *shellModel) ensureHubChatSession() tea.Cmd {
	if m.hubChat != nil && m.hubChatSession != nil {
		return tea.Batch(m.resizeHubChat(), m.hubChat.Init())
	}
	session, err := openHubChatSession()
	if err != nil {
		m.statusMessage = "Open hub chat failed: " + err.Error()
		return nil
	}
	_ = m.closeHubChat()
	chat := newModel(session.Options)
	chat.theme = m.theme
	m.hubChatSession = session
	m.hubChat = &chat
	m.syncHubChatFocus()
	return tea.Batch(m.resizeHubChat(), m.hubChat.Init())
}

func (m shellModel) hubView() string {
	title := m.theme.Accent.Render("  Forge Hub")
	subtitle := m.theme.Muted.Render("  Browse folders and open a workspace.")
	panelHeight := m.height
	if panelHeight <= 0 {
		panelHeight = 32
	}
	innerHeight := m.hubInnerHeight()
	body := clipLines(m.renderHubMain(), m.hubBodyLineBudget())
	bodyLines := strings.Split(body, "\n")
	if len(bodyLines) == 1 && bodyLines[0] == "" {
		bodyLines = nil
	}
	lines := []string{title, subtitle, ""}
	lines = append(lines, bodyLines...)
	footer := []string{m.renderHubHelp(), m.renderHubStatus()}
	filler := innerHeight - len(lines) - len(footer)
	for i := 0; i < filler; i++ {
		lines = append(lines, "")
	}
	lines = append(lines, footer...)
	mainStyle := lipgloss.NewStyle().
		Width(m.hubContentWidth()).
		Height(innerHeight).
		Padding(1, 1)
	return mainStyle.Render(strings.Join(lines, "\n"))
}

func (m shellModel) hubContentWidth() int {
	// Sidebar total width is content + padding + border = shellSidebarWidth+4.
	// This panel also has 1-char horizontal padding on each side, so reserve
	// 2 more columns here to keep the joined layout within the terminal width.
	return max(24, m.width-shellSidebarWidth-6)
}

func (m shellModel) hubTextWidth() int {
	// Be conservative: lipgloss width calculations plus padding/styles can
	// still consume a few extra columns in the joined layout.
	return max(16, m.hubContentWidth()-6)
}

func (m shellModel) hubInnerHeight() int {
	panelHeight := m.height
	if panelHeight <= 0 {
		panelHeight = 32
	}
	return max(1, panelHeight-2)
}

func (m shellModel) hubBodyLineBudget() int {
	// Header consumes 3 lines ("Forge Hub", subtitle, blank), footer
	// consumes 2 fixed lines (help + status).
	return max(1, m.hubInnerHeight()-5)
}

func (m shellModel) truncateHubText(text string) string {
	width := m.hubTextWidth()
	if width <= 0 {
		return text
	}
	return truncateStrict(text, width)
}

func clipLines(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	lines := strings.Split(text, "\n")
	if len(lines) <= limit {
		return text
	}
	return strings.Join(lines[:limit], "\n")
}

func truncateStrict(s string, limit int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if limit <= 0 {
		return ""
	}
	if len(s) <= limit {
		return s
	}
	if limit <= 3 {
		return s[:limit]
	}
	return s[:limit-3] + "..."
}

func (m shellModel) renderHubMain() string {
	switch m.activeView {
	case viewMigration:
		return m.renderMigrationWizard()
	case viewExplorer:
		return m.renderExplorer()
	case viewRecent:
		return m.renderRecent()
	case viewPinned:
		return m.renderPinned()
	case viewSessions:
		return m.renderSessions()
	case viewTools:
		if m.workspace != nil {
			return m.workspace.describeTools()
		}
		return m.theme.Muted.Render("Open a workspace to inspect registered tools.")
	case viewMCPs:
		if m.workspace != nil && m.workspace.options.MCP != nil {
			return m.workspace.options.MCP.Describe()
		}
		return m.theme.Muted.Render("Open a workspace to inspect MCP servers.")
	case viewSettings:
		return m.renderSettings()
	case viewChat:
		return stripAnsi(m.hubChatEmptyView())
	default:
		return ""
	}
}

func (m shellModel) renderExplorer() string {
	lines := []string{
		m.theme.StatusValue.Render(m.truncateHubText("Path: " + m.explorerDir)),
		m.theme.Muted.Render(m.truncateHubText("Enter opens a directory. O opens the selected directory as a workspace. Backspace goes to the parent.")),
		"",
	}
	lines = append(lines, m.explorerVisibleLines()...)
	return strings.Join(lines, "\n")
}

func (m shellModel) explorerVisibleLines() []string {
	// Explorer body spends 3 lines on its own header (path, help, blank).
	// Keep the scroll window aligned with the actual clipped body height so
	// the selected row never slips one line below the visible area.
	visible := max(1, m.hubBodyLineBudget()-3)
	start := 0
	if m.explorerIndex >= visible {
		start = m.explorerIndex - visible + 1
	}
	end := start + visible
	if end > len(m.explorerEntries) {
		end = len(m.explorerEntries)
	}
	lines := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		entry := m.explorerEntries[i]
		name := entry.Name
		if entry.IsDir {
			name += string(os.PathSeparator)
		}
		prefix := "  "
		if i == m.explorerIndex {
			prefix = "> "
		}
		lines = append(lines, prefix+m.truncateHubText(name))
	}
	if len(lines) == 0 {
		lines = append(lines, m.theme.Muted.Render("  No entries"))
	}
	return lines
}

func (m shellModel) renderRecent() string {
	lines := []string{m.theme.Muted.Render("Recent workspaces are persisted outside the project and can be reopened directly."), ""}
	if len(m.hubState.RecentWorkspaces) == 0 {
		lines = append(lines, m.theme.Muted.Render("No recent workspaces yet."))
		return strings.Join(lines, "\n")
	}
	for i, item := range m.hubState.RecentWorkspaces {
		prefix := "  "
		if i == m.recentIndex {
			prefix = "> "
		}
		lines = append(lines, fmt.Sprintf("%s%s", prefix, m.truncateHubText(item.Path)))
		lines = append(lines, m.theme.Muted.Render("    opened "+item.OpenedAt.Format("2006-01-02 15:04")))
	}
	return strings.Join(lines, "\n")
}

func (m shellModel) renderSessions() string {
	cwd := m.explorerDir
	if m.workspace != nil {
		cwd = m.workspace.options.CWD
	}
	items, err := session.List(cwd, 10)
	if err != nil {
		return m.theme.ErrorStyle.Render("Session list failed: " + err.Error())
	}
	if len(items) == 0 {
		return m.theme.Muted.Render("No sessions found in " + cwd)
	}
	rows := make([][]string, 0, len(items))
	for _, item := range items {
		rows = append(rows, []string{
			item.ID,
			fmt.Sprintf("%d", item.EventCount),
			item.UpdatedAt.Format("2006-01-02 15:04"),
		})
	}
	return m.theme.FormatTable([]string{"Session", "Events", "Updated"}, rows)
}

func (m shellModel) renderSettings() string {
	if formView := m.activeHubFormView(); formView != "" {
		var label string
		items := m.hubSettingsItems()
		if m.hubSettingsIndex >= 0 && m.hubSettingsIndex < len(items) {
			if items[m.hubSettingsIndex].Scope == scopeHub {
				label = "Editing Hub default (~/.codex/forge/global.toml)"
			} else {
				label = "Editing workspace override: " + m.hubSettingsTarget()
			}
		}
		return m.theme.Muted.Render(label) + "\n\n" + formView
	}
	items := m.hubSettingsItems()

	lines := []string{
		m.theme.StatusValue.Render(m.truncateHubText("Hub default file: " + compactDisplayPath(globalConfigDisplayPath()))),
		m.theme.StatusValue.Render(m.truncateHubText("Workspace target: " + compactDisplayPath(m.hubSettingsTarget()))),
		"",
	}

	// Render two sections with headers so the user can tell at a glance
	// whether selecting an item will edit the global file or the workspace
	// toml.
	currentScope := hubScope(-1)
	for i, item := range items {
		if item.Scope != currentScope {
			currentScope = item.Scope
			if i > 0 {
				lines = append(lines, "")
			}
			switch item.Scope {
			case scopeHub:
				lines = append(lines, m.theme.Accent.Render("HUB DEFAULTS"))
			case scopeWorkspace:
				lines = append(lines, m.theme.Accent.Render("WORKSPACE OVERRIDES"))
			}
		}
		prefix := "  "
		if i == m.hubSettingsIndex && m.activePane == paneMain {
			prefix = "> "
		}
		lines = append(lines, prefix+item.Label)
		lines = append(lines, m.theme.Muted.Render("    "+item.Hint))
	}
	lines = append(lines, "", m.theme.Muted.Render(m.truncateHubText("Enter opens the selected editor. Hub items persist globally; Workspace items write to the target's .forge/config.toml.")))
	return strings.Join(lines, "\n")
}

// globalConfigDisplayPath shows the hub global config file path with the
// home dir abbreviated to ~ for readability. compactDisplayPath does the
// rest of the cosmetics.
func globalConfigDisplayPath() string {
	return globalconfig.Path()
}

func (m shellModel) renderHubStatus() string {
	parts := []string{
		"HUB",
		"pane:" + string(m.activePane),
		"view:" + string(m.activeView),
	}
	if m.activeView == viewSettings {
		parts = append(parts, "target:"+compactDisplayPath(m.hubSettingsTarget()))
	}
	if status := compactStatusMessage(m.statusMessage); status != "" {
		parts = append(parts, status)
	}
	width := max(8, m.hubTextWidth()-2) // status bar adds horizontal padding
	return m.theme.StatusBar.Render(truncateStrict(" "+strings.Join(parts, " | "), width))
}

func (m shellModel) renderHubHelp() string {
	var text string
	if m.activePane == paneSidebar {
		text = "Hub keys: Up/Down select view | Enter activate | Tab switch pane | Esc explorer/quit"
	} else {
		switch m.activeView {
		case viewExplorer:
			text = "Explorer: Up/Down move | Enter open dir | O open workspace | Backspace parent | P pin | Esc explorer/quit"
		case viewRecent:
			text = "Recent: Up/Down move | Enter open workspace | Esc explorer | Tab switch pane"
		case viewPinned:
			text = "Pinned: Up/Down move | Enter open workspace | P unpin | Esc explorer | Tab switch pane"
		case viewSettings:
			if m.activeHubForm != hubFormNone {
				text = "Settings form: Enter confirm | Esc cancel | Tab or arrows navigate"
			} else {
				text = "Settings: Up/Down select item | Enter edit | Esc explorer | Tab switch pane"
			}
		case viewChat:
			text = "Hub Chat: general conversation | Ctrl+W or F6 switch pane | Esc explorer | Tab switch pane"
		case viewSessions:
			text = "Sessions: review saved workspace sessions | Esc explorer | Tab switch pane"
		case viewTools:
			text = "Tools: inspect available tools | Esc explorer | Tab switch pane"
		case viewMCPs:
			text = "MCPs: inspect connected servers | Esc explorer | Tab switch pane"
		case viewMigration:
			text = "Migration: Enter apply | Esc dismiss"
		default:
			text = "Hub: Tab switch pane | Esc explorer/quit"
		}
	}
	return m.theme.Muted.Render(m.truncateHubText(text))
}

func compactStatusMessage(msg string) string {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return ""
	}
	lines := strings.Split(msg, "\n")
	parts := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.Join(strings.Fields(strings.TrimSpace(line)), " ")
		if line != "" {
			parts = append(parts, line)
		}
	}
	return strings.Join(parts, " | ")
}

type hubScope int

const (
	scopeHub hubScope = iota
	scopeWorkspace
)

type hubSettingsItem struct {
	Label string
	Hint  string
	Scope hubScope
	Open  func(*shellModel)
}

func (m *shellModel) hubSettingsItems() []hubSettingsItem {
	return []hubSettingsItem{
		// Hub defaults: persist to ~/.codex/forge/global.toml. Apply to
		// every workspace that does not override.
		{
			Label: "Theme (global)",
			Hint:  "persist UI theme as default for every workspace",
			Scope: scopeHub,
			Open: func(m *shellModel) {
				m.themeForm = newThemeForm(m.theme)
				m.activeHubForm = hubFormTheme
			},
		},
		{
			Label: "Skills (global)",
			Hint:  "browse and install skills into ~/.codex/skills",
			Scope: scopeHub,
			Open: func(m *shellModel) {
				m.openHubSkillsBrowser()
			},
		},

		// Workspace overrides: persist to <target>/.forge/config.toml.
		// Hub defaults still apply to keys this file does not write.
		{
			Label: "Provider",
			Hint:  "base URL, key, chat model",
			Scope: scopeHub,
			Open: func(m *shellModel) {
				if cfg, ok := m.loadHubSettingsConfig(); ok {
					m.providerForm = newProviderForm("", cfg, m.theme)
					m.activeHubForm = hubFormProvider
				}
			},
		},
		{
			Label: "Model",
			Hint:  "pick active chat model and context",
			Scope: scopeHub,
			Open: func(m *shellModel) {
				if cfg, ok := m.loadHubSettingsConfig(); ok {
					m.modelForm = newModelForm("", cfg, hubSettingsProviders(cfg), m.theme)
					m.activeHubForm = hubFormModel
				}
			},
		},
		{
			Label: "Model Multi",
			Hint:  "role models and loading strategy",
			Scope: scopeHub,
			Open: func(m *shellModel) {
				if cfg, ok := m.loadHubSettingsConfig(); ok {
					m.modelMultiForm = newModelMultiFormWithPersist("", cfg, hubSettingsProviders(cfg), m.theme, false)
					m.activeHubForm = hubFormModelMulti
				}
			},
		},
		{
			Label: "YARN / Context",
			Hint:  "context, budget, pins, compacting",
			Scope: scopeHub,
			Open: func(m *shellModel) {
				if cfg, ok := m.loadHubSettingsConfig(); ok {
					m.yarnSettingsForm = newYarnSettingsForm("", cfg, m.theme)
					m.activeHubForm = hubFormYarn
				}
			},
		},
		{
			Label: "Open Workspace",
			Hint:  "enter chat with this target",
			Scope: scopeWorkspace,
			Open: func(m *shellModel) {
				m.openSelectedWorkspace()
			},
		},
	}
}

func (m *shellModel) loadHubSettingsConfig() (config.Config, bool) {
	cfg, err := loadHubGlobalConfig()
	if err != nil {
		m.statusMessage = "Config load failed: " + err.Error()
		return config.Config{}, false
	}
	return cfg, true
}

func (m shellModel) hubSettingsTarget() string {
	if m.workspace != nil {
		return m.workspace.options.CWD
	}
	if m.activeView == viewRecent && m.recentIndex >= 0 && m.recentIndex < len(m.hubState.RecentWorkspaces) {
		return normalizeDir(m.hubState.RecentWorkspaces[m.recentIndex].Path)
	}
	if len(m.explorerEntries) > 0 {
		entry := m.explorerEntries[m.explorerIndex]
		if entry.IsDir {
			return normalizeDir(entry.Path)
		}
	}
	return normalizeDir(m.explorerDir)
}

func hubSettingsProviders(cfg config.Config) *llm.Registry {
	providers := llm.NewRegistry()
	providers.Register(llm.NewOpenAICompatible("openai_compatible", cfg.Providers.OpenAICompatible))
	providers.Register(llm.NewOpenAICompatible("lmstudio", cfg.Providers.LMStudio))
	return providers
}

func (m *shellModel) loadExplorerDir(dir string) {
	dir = normalizeDir(dir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		m.statusMessage = "Explorer error: " + err.Error()
		return
	}
	list := make([]explorerEntry, 0, len(entries))
	for _, entry := range entries {
		list = append(list, explorerEntry{
			Name:  entry.Name(),
			Path:  filepath.Join(dir, entry.Name()),
			IsDir: entry.IsDir(),
		})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].IsDir != list[j].IsDir {
			return list[i].IsDir
		}
		return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name)
	})
	m.explorerDir = dir
	m.explorerEntries = list
	m.explorerIndex = 0
	m.hubState.LastHubDir = dir
	m.saveHubState()
}

func (m *shellModel) moveExplorerParent() {
	parent := filepath.Dir(m.explorerDir)
	if parent == "" || parent == m.explorerDir {
		return
	}
	m.loadExplorerDir(parent)
}

func (m *shellModel) enterExplorerSelection() {
	if len(m.explorerEntries) == 0 {
		return
	}
	entry := m.explorerEntries[m.explorerIndex]
	if entry.IsDir {
		m.loadExplorerDir(entry.Path)
	}
}

func (m *shellModel) openSelectedWorkspace() {
	if m.activeView == viewSettings || m.activeView == viewRecent {
		target := m.hubSettingsTarget()
		if err := m.OpenWorkspace(target); err != nil {
			m.statusMessage = "Open workspace failed: " + err.Error()
		}
		return
	}
	target := m.explorerDir
	if len(m.explorerEntries) > 0 {
		entry := m.explorerEntries[m.explorerIndex]
		if entry.IsDir {
			target = entry.Path
		}
	}
	if err := m.OpenWorkspace(target); err != nil {
		m.statusMessage = "Open workspace failed: " + err.Error()
	}
}

func (m *shellModel) openRecentWorkspace() {
	if m.recentIndex < 0 || m.recentIndex >= len(m.hubState.RecentWorkspaces) {
		return
	}
	if err := m.OpenWorkspace(m.hubState.RecentWorkspaces[m.recentIndex].Path); err != nil {
		m.statusMessage = "Open workspace failed: " + err.Error()
	}
}

func (m *shellModel) recordRecentWorkspace(cwd string) {
	cwd = normalizeDir(cwd)
	m.hubState.LastHubDir = filepath.Dir(cwd)
	now := time.Now().UTC()
	filtered := make([]RecentWorkspace, 0, len(m.hubState.RecentWorkspaces)+1)
	filtered = append(filtered, RecentWorkspace{Path: cwd, OpenedAt: now})
	for _, item := range m.hubState.RecentWorkspaces {
		if normalizeDir(item.Path) == cwd {
			continue
		}
		filtered = append(filtered, item)
		if len(filtered) >= 10 {
			break
		}
	}
	m.hubState.RecentWorkspaces = filtered
	m.saveHubState()
}

func (m *shellModel) saveHubState() {
	if m.options.StateStore == nil {
		return
	}
	_ = m.options.StateStore.Save(m.hubState)
}

func normalizeDir(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}
