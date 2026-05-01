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

	viewExplorer appView = "explorer"
	viewRecent   appView = "recent"
	viewSessions appView = "sessions"
	viewTools    appView = "tools"
	viewMCPs     appView = "mcps"
	viewSettings appView = "settings"
	viewChat     appView = "chat"
	viewPlan     appView = "plan"
	viewDiff     appView = "diff"
	viewHub      appView = "hub"
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
	options          ShellOptions
	theme            Theme
	width            int
	height           int
	mode             appMode
	activePane       appPane
	activeView       appView
	sidebarIndex     int
	hubState         HubState
	explorerDir      string
	explorerEntries  []explorerEntry
	explorerIndex    int
	recentIndex      int
	workspace        *model
	workspaceSession *WorkspaceSession
	activeHubForm    hubFormMode
	providerForm     providerForm
	modelForm        modelForm
	modelMultiForm   modelMultiForm
	yarnSettingsForm yarnSettingsForm
	hubSettingsIndex int
	statusMessage    string
	lastEscTime      time.Time
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
		return m, m.resizeWorkspace()
	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlC {
			return m, tea.Quit
		}
		if m.mode == modeWorkspace && m.activePane == paneInput && !m.workspaceHasModal() {
			if msg.String() == "ctrl+w" || msg.Type == tea.KeyF6 {
				m.rotatePane(1)
				return m, m.resizeWorkspace()
			}
			return m, m.forwardWorkspace(msg)
		}
		if m.workspaceHasModal() {
			return m, m.forwardWorkspace(msg)
		}
		return m.handleKey(msg)
	default:
		if m.workspace != nil {
			return m, m.forwardWorkspace(msg)
		}
	}
	return m, nil
}

func (m shellModel) View() string {
	if m.workspace != nil && m.workspaceHasModal() {
		return m.workspace.View()
	}
	sidebar := m.sidebarView()
	if m.mode == modeWorkspace && m.workspace != nil {
		return lipgloss.JoinHorizontal(lipgloss.Top, sidebar, m.workspace.View())
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, sidebar, m.hubView())
}

func (m *shellModel) Close() error {
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
		if m.mode == modeHub && m.activePane == paneMain && len(msg.Runes) == 1 {
			switch strings.ToLower(string(msg.Runes[0])) {
			case "o":
				m.openSelectedWorkspace()
				return *m, m.resizeWorkspace()
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
	if m.activeView != viewExplorer {
		m.activeView = viewExplorer
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
			m.sidebarIndex--
			m.activeView = m.currentSidebarItems()[m.sidebarIndex].View
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
			m.sidebarIndex++
			m.activeView = items[m.sidebarIndex].View
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
		m.activeView = m.currentSidebarItems()[m.sidebarIndex].View
		return *m, nil
	}
	if m.mode == modeHub && m.activePane == paneMain {
		switch m.activeView {
		case viewExplorer:
			m.enterExplorerSelection()
			return *m, nil
		case viewRecent:
			m.openRecentWorkspace()
			return *m, m.resizeWorkspace()
		case viewSettings:
			items := m.hubSettingsItems()
			if m.hubSettingsIndex >= 0 && m.hubSettingsIndex < len(items) {
				items[m.hubSettingsIndex].Open(m)
			}
			return *m, nil
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
	if m.mode == modeWorkspace {
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
		Height(max(10, m.height)).
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
		{View: viewRecent, Label: "Recent", Hint: "reopen quickly"},
		{View: viewSessions, Label: "Sessions", Hint: "workspace session logs"},
		{View: viewTools, Label: "Tools", Hint: "global info"},
		{View: viewMCPs, Label: "MCPs", Hint: "global info"},
		{View: viewSettings, Label: "Settings", Hint: "hub state"},
	}
}

func (m *shellModel) selectSidebarView(view appView) {
	items := m.currentSidebarItems()
	for i, item := range items {
		if item.View == view {
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
		m.pushWorkspacePanelOutput("status", m.workspace.describeStatus())
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

func (m shellModel) hubView() string {
	title := m.theme.Accent.Render("  Forge Hub")
	subtitle := m.theme.Muted.Render("  Browse folders and open a workspace.")
	body := m.renderHubMain()
	status := m.renderHubStatus()
	mainStyle := lipgloss.NewStyle().
		Width(max(40, m.width-shellSidebarWidth-1)).
		Height(max(10, m.height)).
		Padding(1, 1)
	return mainStyle.Render(title + "\n" + subtitle + "\n\n" + body + "\n\n" + status)
}

func (m shellModel) renderHubMain() string {
	switch m.activeView {
	case viewExplorer:
		return m.renderExplorer()
	case viewRecent:
		return m.renderRecent()
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
	default:
		return ""
	}
}

func (m shellModel) renderExplorer() string {
	lines := []string{
		m.theme.StatusValue.Render("Path: ") + m.explorerDir,
		m.theme.Muted.Render("Enter opens a directory. O opens the selected directory as a workspace. Backspace goes to the parent."),
		"",
	}
	lines = append(lines, m.explorerVisibleLines()...)
	return strings.Join(lines, "\n")
}

func (m shellModel) explorerVisibleLines() []string {
	visible := max(6, m.height-10)
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
		lines = append(lines, prefix+name)
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
		lines = append(lines, fmt.Sprintf("%s%s", prefix, item.Path))
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
		header := m.theme.Muted.Render("Editing " + m.hubSettingsTarget())
		return header + "\n\n" + formView
	}
	items := m.hubSettingsItems()
	lines := []string{
		m.theme.Muted.Render("Settings edit the target workspace config directly from Hub."),
		m.theme.StatusValue.Render("Target: ") + compactDisplayPath(m.hubSettingsTarget()),
		"",
	}
	for i, item := range items {
		prefix := "  "
		if i == m.hubSettingsIndex && m.activePane == paneMain {
			prefix = "> "
		}
		lines = append(lines, prefix+item.Label)
		lines = append(lines, m.theme.Muted.Render("    "+item.Hint))
	}
	lines = append(lines, "", m.theme.Muted.Render("Enter opens the selected editor. Use Explorer or Recent to change the target."))
	return strings.Join(lines, "\n")
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
	if strings.TrimSpace(m.statusMessage) != "" {
		parts = append(parts, m.statusMessage)
	}
	return m.theme.StatusBar.Render(" " + strings.Join(parts, " | "))
}

type hubSettingsItem struct {
	Label string
	Hint  string
	Open  func(*shellModel)
}

func (m *shellModel) hubSettingsItems() []hubSettingsItem {
	return []hubSettingsItem{
		{
			Label: "Provider",
			Hint:  "base URL, key, chat model",
			Open: func(m *shellModel) {
				if cfg, ok := m.loadHubSettingsConfig(); ok {
					m.providerForm = newProviderForm(m.hubSettingsTarget(), cfg, m.theme)
					m.activeHubForm = hubFormProvider
				}
			},
		},
		{
			Label: "Model",
			Hint:  "pick active chat model and context",
			Open: func(m *shellModel) {
				if cfg, ok := m.loadHubSettingsConfig(); ok {
					m.modelForm = newModelForm(m.hubSettingsTarget(), cfg, hubSettingsProviders(cfg), m.theme)
					m.activeHubForm = hubFormModel
				}
			},
		},
		{
			Label: "Model Multi",
			Hint:  "role models and loading strategy",
			Open: func(m *shellModel) {
				if cfg, ok := m.loadHubSettingsConfig(); ok {
					m.modelMultiForm = newModelMultiForm(m.hubSettingsTarget(), cfg, hubSettingsProviders(cfg), m.theme)
					m.activeHubForm = hubFormModelMulti
				}
			},
		},
		{
			Label: "YARN / Context",
			Hint:  "context, budget, pins, compacting",
			Open: func(m *shellModel) {
				if cfg, ok := m.loadHubSettingsConfig(); ok {
					m.yarnSettingsForm = newYarnSettingsForm(m.hubSettingsTarget(), cfg, m.theme)
					m.activeHubForm = hubFormYarn
				}
			},
		},
		{
			Label: "Open Workspace",
			Hint:  "enter chat with this target",
			Open: func(m *shellModel) {
				m.openSelectedWorkspace()
			},
		},
	}
}

func (m *shellModel) loadHubSettingsConfig() (config.Config, bool) {
	target := m.hubSettingsTarget()
	cfg, err := config.Load(target)
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
