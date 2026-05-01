package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forge/internal/config"
	"forge/internal/globalconfig"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestShellStartsInHubWithoutWorkspace(t *testing.T) {
	store := &memoryHubStateStore{}
	m := newShellModel(ShellOptions{
		InitialHubDir: t.TempDir(),
		StateStore:    store,
	})
	if m.mode != modeHub {
		t.Fatalf("mode = %s, want %s", m.mode, modeHub)
	}
	if m.workspace != nil {
		t.Fatal("expected no workspace at startup")
	}
	if m.activeView != viewExplorer {
		t.Fatalf("activeView = %s, want %s", m.activeView, viewExplorer)
	}
}

func TestShellStartsInWorkspaceWhenProvided(t *testing.T) {
	cwd := t.TempDir()
	m := newShellModel(ShellOptions{
		InitialHubDir:    cwd,
		InitialWorkspace: &WorkspaceSession{Options: Options{CWD: cwd}},
		StateStore:       &memoryHubStateStore{},
	})
	t.Cleanup(func() {
		_ = (&m).Close()
	})
	if m.mode != modeWorkspace {
		t.Fatalf("mode = %s, want %s", m.mode, modeWorkspace)
	}
	if m.workspace == nil {
		t.Fatal("expected workspace model")
	}
	if m.workspace.options.CWD != cwd {
		t.Fatalf("workspace cwd = %q, want %q", m.workspace.options.CWD, cwd)
	}
	if m.activePane != paneInput {
		t.Fatalf("activePane = %s, want %s", m.activePane, paneInput)
	}
}

func TestShellOpenWorkspaceClosesPreviousSession(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	closeCount := 0
	store := &memoryHubStateStore{}
	m := newShellModel(ShellOptions{
		InitialHubDir: dirA,
		StateStore:    store,
		OpenWorkspace: func(cwd, resume string) (*WorkspaceSession, error) {
			return &WorkspaceSession{
				Options: Options{CWD: cwd},
				CloseFunc: func() error {
					closeCount++
					return nil
				},
			}, nil
		},
	})
	t.Cleanup(func() {
		_ = (&m).Close()
	})

	if err := m.OpenWorkspace(dirA); err != nil {
		t.Fatal(err)
	}
	if err := m.OpenWorkspace(dirB); err != nil {
		t.Fatal(err)
	}
	if closeCount != 1 {
		t.Fatalf("closeCount = %d, want 1", closeCount)
	}
	if got := m.workspace.options.CWD; got != dirB {
		t.Fatalf("workspace cwd = %q, want %q", got, dirB)
	}
	if len(store.state.RecentWorkspaces) == 0 || store.state.RecentWorkspaces[0].Path != dirB {
		t.Fatalf("recent workspaces not updated: %+v", store.state.RecentWorkspaces)
	}
}

func TestShellSwitchToHubClosesWorkspace(t *testing.T) {
	cwd := t.TempDir()
	closeCount := 0
	m := newShellModel(ShellOptions{
		InitialHubDir: cwd,
		InitialWorkspace: &WorkspaceSession{
			Options: Options{CWD: cwd},
			CloseFunc: func() error {
				closeCount++
				return nil
			},
		},
		StateStore: &memoryHubStateStore{},
	})

	if err := m.SwitchToHub(); err != nil {
		t.Fatal(err)
	}
	if closeCount != 1 {
		t.Fatalf("closeCount = %d, want 1", closeCount)
	}
	if m.mode != modeHub {
		t.Fatalf("mode = %s, want %s", m.mode, modeHub)
	}
	if m.workspace != nil {
		t.Fatal("expected workspace closed")
	}
}

func TestShellExplorerNavigatesDirectories(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "repo")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	m := newShellModel(ShellOptions{
		InitialHubDir: root,
		StateStore:    &memoryHubStateStore{},
	})
	if len(m.explorerEntries) == 0 {
		t.Fatal("expected explorer entries")
	}
	m.enterExplorerSelection()
	if m.explorerDir != child {
		t.Fatalf("explorerDir = %q, want %q", m.explorerDir, child)
	}
	m.moveExplorerParent()
	if m.explorerDir != root {
		t.Fatalf("explorerDir after parent = %q, want %q", m.explorerDir, root)
	}
}

func TestShellTabDoesNotLeaveWorkspaceInputPane(t *testing.T) {
	cwd := t.TempDir()
	m := newShellModel(ShellOptions{
		InitialHubDir:    cwd,
		InitialWorkspace: &WorkspaceSession{Options: Options{CWD: cwd}},
		StateStore:       &memoryHubStateStore{},
	})
	t.Cleanup(func() {
		_ = (&m).Close()
	})
	m.activePane = paneInput

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	next := updated.(shellModel)
	if next.activePane != paneInput {
		t.Fatalf("activePane = %s, want %s", next.activePane, paneInput)
	}
}

func TestShellCtrlWSwitchesWorkspacePane(t *testing.T) {
	cwd := t.TempDir()
	m := newShellModel(ShellOptions{
		InitialHubDir:    cwd,
		InitialWorkspace: &WorkspaceSession{Options: Options{CWD: cwd}},
		StateStore:       &memoryHubStateStore{},
	})
	t.Cleanup(func() {
		_ = (&m).Close()
	})
	m.activePane = paneInput

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlW})
	next := updated.(shellModel)
	if next.activePane == paneInput {
		t.Fatalf("expected ctrl+w to rotate away from input pane")
	}
}

func TestShellSelectingChatFromSidebarFocusesInput(t *testing.T) {
	cwd := t.TempDir()
	m := newShellModel(ShellOptions{
		InitialHubDir:    cwd,
		InitialWorkspace: &WorkspaceSession{Options: Options{CWD: cwd}},
		StateStore:       &memoryHubStateStore{},
	})
	t.Cleanup(func() {
		_ = (&m).Close()
	})
	m.activePane = paneSidebar
	m.activeView = viewPlan
	m.sidebarIndex = 0

	updated, _ := m.handleEnter()
	next := updated.(shellModel)
	if next.activeView != viewChat {
		t.Fatalf("activeView = %s, want %s", next.activeView, viewChat)
	}
	if next.activePane != paneInput {
		t.Fatalf("activePane = %s, want %s", next.activePane, paneInput)
	}
}

func TestShellEnterInWorkspaceInputForwardsToWorkspace(t *testing.T) {
	cwd := t.TempDir()
	m := newShellModel(ShellOptions{
		InitialHubDir:    cwd,
		InitialWorkspace: &WorkspaceSession{Options: Options{CWD: cwd}},
		StateStore:       &memoryHubStateStore{},
	})
	t.Cleanup(func() {
		_ = (&m).Close()
	})
	m.activePane = paneInput
	m.workspace.input.SetValue("/help")

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	next := updated.(shellModel)
	if got := next.workspace.input.Value(); got != "" {
		t.Fatalf("workspace input = %q, want empty after enter", got)
	}
}

func TestShellEnterInHubSidebarActivatesMainPane(t *testing.T) {
	m := newShellModel(ShellOptions{
		InitialHubDir: t.TempDir(),
		StateStore:    &memoryHubStateStore{},
	})
	m.activePane = paneSidebar
	m.sidebarIndex = 1 // Pinned

	updated, _ := m.handleEnter()
	next := updated.(shellModel)
	if next.activeView != viewPinned {
		t.Fatalf("activeView = %s, want %s", next.activeView, viewPinned)
	}
	if next.activePane != paneMain {
		t.Fatalf("activePane = %s, want %s", next.activePane, paneMain)
	}
}

func TestHubSidebarIncludesChatBelowSettings(t *testing.T) {
	m := newShellModel(ShellOptions{
		InitialHubDir: t.TempDir(),
		StateStore:    &memoryHubStateStore{},
	})
	items := m.currentSidebarItems()
	if len(items) < 2 {
		t.Fatal("expected hub sidebar items")
	}
	last := items[len(items)-1]
	prev := items[len(items)-2]
	if prev.View != viewSettings || last.View != viewChat {
		t.Fatalf("expected Settings then Chat at end, got prev=%s last=%s", prev.View, last.View)
	}
}

func TestShellEnterInHubSidebarOpensChat(t *testing.T) {
	globalHome := t.TempDir()
	t.Setenv("FORGE_GLOBAL_HOME", globalHome)
	cwd := t.TempDir()
	m := newShellModel(ShellOptions{
		InitialHubDir: cwd,
		StateStore:    &memoryHubStateStore{},
	})
	t.Cleanup(func() {
		_ = (&m).Close()
	})
	m.activePane = paneSidebar
	m.sidebarIndex = len(m.currentSidebarItems()) - 1 // Chat

	updated, cmd := m.handleEnter()
	next := updated.(shellModel)
	if next.activeView != viewChat {
		t.Fatalf("activeView = %s, want %s", next.activeView, viewChat)
	}
	if next.activePane != paneInput {
		t.Fatalf("activePane = %s, want %s", next.activePane, paneInput)
	}
	if next.hubChat == nil {
		t.Fatal("expected hub chat model to be created")
	}
	if got, want := next.hubChat.options.CWD, hubChatRootDir(); got != want {
		t.Fatalf("hub chat cwd = %q, want %q", got, want)
	}
	if next.hubChat.options.CWD == cwd {
		t.Fatalf("hub chat should not reuse explorer cwd %q", cwd)
	}
	if cmd == nil {
		t.Fatal("expected resize/init command for hub chat")
	}
}

func TestHubChatIgnoresSelectedExplorerTarget(t *testing.T) {
	t.Setenv("FORGE_GLOBAL_HOME", t.TempDir())
	root := t.TempDir()
	child := filepath.Join(root, "repo")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	m := newShellModel(ShellOptions{
		InitialHubDir: root,
		StateStore:    &memoryHubStateStore{},
	})
	t.Cleanup(func() {
		_ = (&m).Close()
	})
	if len(m.explorerEntries) == 0 {
		t.Fatal("expected explorer entries")
	}
	m.explorerIndex = 0
	m.activePane = paneSidebar
	m.sidebarIndex = len(m.currentSidebarItems()) - 1 // Chat

	updated, _ := m.handleEnter()
	next := updated.(shellModel)
	if next.hubChat == nil {
		t.Fatal("expected hub chat model")
	}
	if got, want := next.hubChat.options.CWD, hubChatRootDir(); got != want {
		t.Fatalf("hub chat cwd = %q, want %q", got, want)
	}
	if got := next.hubChat.options.CWD; got == child {
		t.Fatalf("hub chat should not bind to selected explorer folder %q", child)
	}
}

func TestApplyHubChatConfigRefreshesOpenChat(t *testing.T) {
	t.Setenv("FORGE_GLOBAL_HOME", t.TempDir())
	m := newShellModel(ShellOptions{
		InitialHubDir: t.TempDir(),
		StateStore:    &memoryHubStateStore{},
	})
	t.Cleanup(func() {
		_ = (&m).Close()
	})
	if cmd := m.ensureHubChatSession(); cmd == nil {
		t.Fatal("expected hub chat session command")
	}
	if m.hubChat == nil {
		t.Fatal("expected hub chat model")
	}
	cfg := m.hubChat.options.Config
	cfg.Models["chat"] = "hub-chat"
	cfg.Models["planner"] = "hub-plan"
	cfg.ModelLoading.Enabled = true
	cfg.ModelLoading.Strategy = "parallel"

	m.applyHubChatConfig(cfg)

	if !m.hubChat.options.Config.ModelLoading.Enabled {
		t.Fatal("expected updated hub chat config to enable model loading")
	}
	if got := m.hubChat.agentRuntime.Config.Models["planner"]; got != "hub-plan" {
		t.Fatalf("planner model = %q, want %q", got, "hub-plan")
	}
	if got := m.hubChat.agentRuntime.Config.Models["chat"]; got != "hub-chat" {
		t.Fatalf("chat model = %q, want %q", got, "hub-chat")
	}
}

func TestDescribeWorkspaceSettingsShowsHubInheritedValues(t *testing.T) {
	t.Setenv("FORGE_GLOBAL_HOME", t.TempDir())
	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, ".forge"), 0o755); err != nil {
		t.Fatal(err)
	}
	modelID := "hub-chat"
	profile := "26B"
	if err := globalconfig.Save(globalconfig.GlobalConfig{
		Models: map[string]string{"chat": modelID},
		Yarn:   &globalconfig.YarnDefaults{Profile: &profile},
	}); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadWithGlobal(cwd)
	if err != nil {
		t.Fatal(err)
	}
	config.InheritChatModelDefaults(&cfg)
	m := newModel(Options{CWD: cwd, Config: cfg})
	t.Cleanup(func() {
		if m.agentRuntime != nil {
			_ = m.agentRuntime.Close()
		}
	})

	output := stripAnsi(m.describeWorkspaceSettings())
	if !strings.Contains(output, "chat_model") || !strings.Contains(output, modelID) || !strings.Contains(output, "hub") {
		t.Fatalf("expected hub-inherited workspace settings, got:\n%s", output)
	}
}

func TestRenderHubStatusCompactsMultilineStatusMessage(t *testing.T) {
	m := newShellModel(ShellOptions{
		InitialHubDir: t.TempDir(),
		StateStore:    &memoryHubStateStore{},
	})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 180, Height: 24})
	m = updated.(shellModel)
	m.activeView = viewExplorer
	m.statusMessage = "explorer = qwen\nplanner = qwen-long\nreviewer = mistral"

	got := stripAnsi(m.renderHubStatus())
	if strings.Contains(got, "\n") {
		t.Fatalf("hub status should stay single-line, got:\n%s", got)
	}
	if !strings.Contains(got, "explorer = qwen | planner = qwen-long | reviewer = mistral") {
		t.Fatalf("hub status did not compact multiline message:\n%s", got)
	}
}

func TestSelectingDifferentHubViewClearsTransientStatusMessage(t *testing.T) {
	m := newShellModel(ShellOptions{
		InitialHubDir: t.TempDir(),
		StateStore:    &memoryHubStateStore{},
	})
	m.activeView = viewSettings
	m.selectSidebarView(viewSettings)
	m.statusMessage = "saved model multi"

	m.selectSidebarView(viewExplorer)

	if m.activeView != viewExplorer {
		t.Fatalf("activeView = %s, want %s", m.activeView, viewExplorer)
	}
	if m.statusMessage != "" {
		t.Fatalf("statusMessage = %q, want cleared after leaving settings", m.statusMessage)
	}
}

func TestHubViewFitsTerminalHeightWithMultilineStatusMessage(t *testing.T) {
	const terminalHeight = 24
	m := newShellModel(ShellOptions{
		InitialHubDir: t.TempDir(),
		StateStore:    &memoryHubStateStore{},
	})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 96, Height: terminalHeight})
	m = updated.(shellModel)
	m.activeView = viewSettings
	m.statusMessage = "explorer = qwen\nplanner = qwen-long\nreviewer = mistral"

	if got := lipgloss.Height(m.View()); got > terminalHeight {
		t.Fatalf("view height = %d, want <= %d\n%s", got, terminalHeight, stripAnsi(m.View()))
	}
}

func TestExplorerKeepsSelectedRowVisibleWhenScrollingDown(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 20; i++ {
		if err := os.Mkdir(filepath.Join(root, fmt.Sprintf("dir-%02d", i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	m := newShellModel(ShellOptions{
		InitialHubDir: root,
		StateStore:    &memoryHubStateStore{},
	})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 96, Height: 14})
	m = updated.(shellModel)
	m.activeView = viewExplorer
	m.explorerIndex = 8

	lines := m.explorerVisibleLines()
	found := false
	for _, line := range lines {
		if strings.HasPrefix(stripAnsi(line), "> ") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected selected explorer row to remain visible, got:\n%s", strings.Join(lines, "\n"))
	}
}

func TestHubViewShowsContextualExplorerHelp(t *testing.T) {
	m := newShellModel(ShellOptions{
		InitialHubDir: t.TempDir(),
		StateStore:    &memoryHubStateStore{},
	})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	m = updated.(shellModel)
	m.activePane = paneMain
	m.activeView = viewExplorer

	view := stripAnsi(m.hubView())
	if !strings.Contains(view, "Explorer: Up/Down move") || !strings.Contains(view, "Enter open dir") {
		t.Fatalf("expected explorer help footer, got:\n%s", view)
	}
}

func TestHubStatusStaysOnBottomLine(t *testing.T) {
	m := newShellModel(ShellOptions{
		InitialHubDir: t.TempDir(),
		StateStore:    &memoryHubStateStore{},
	})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	m = updated.(shellModel)
	m.activePane = paneSidebar
	m.activeView = viewExplorer

	lines := strings.Split(stripAnsi(m.hubView()), "\n")
	last := ""
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			last = lines[i]
			break
		}
	}
	if !strings.Contains(last, "HUB | pane:sidebar | view:explorer") {
		t.Fatalf("expected hub status on bottom line, got last non-empty line:\n%s\n\nfull view:\n%s", last, strings.Join(lines, "\n"))
	}
}

type memoryHubStateStore struct {
	state HubState
}

func (s *memoryHubStateStore) Load() (HubState, error) {
	return s.state, nil
}

func (s *memoryHubStateStore) Save(state HubState) error {
	s.state = state
	return nil
}
