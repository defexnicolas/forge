package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"forge/internal/skills"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type skillsForm struct {
	search           textinput.Model
	allResults       []skills.Skill
	installedResults []skills.Skill
	filtered         []skills.Skill
	selected         int
	offset           int
	cwd              string
	done             bool
	canceled         bool
	loading          bool
	installed        string
	removed          string
	errMsg           string
	notice           string
	confirmRemove    string
	manager          *skills.Manager
	theme            Theme
	repos            []string
	directory        bool
	tab              skillsTab
}

type skillsTab int

const (
	skillsTabAvailable skillsTab = iota
	skillsTabInstalled
)

type skillsLoadedMsg struct {
	found []skills.Skill
	err   error
	repos []string
}

func newSkillsForm(cwd string, mgr *skills.Manager, theme Theme, repos []string, force bool) (skillsForm, tea.Cmd) {
	search := textinput.New()
	search.Placeholder = "Search skills..."
	search.Focus()
	search.Width = 50
	search.Prompt = "  > "

	directory := len(repos) == 0
	repos = normalizeSkillRepos(mgr, repos)
	all, cached := cachedSkills(mgr, repos, directory)
	notice := cacheNotice(mgr, repos, directory)
	loading := force || !cached
	if len(all) == 0 {
		notice = "fetching Skills CLI; showing offline fallback for now"
		all = fallbackSkills(mgr)
	} else if force {
		notice = "refreshing Skills CLI cache"
	}

	form := skillsForm{
		search:           search,
		allResults:       all,
		installedResults: mgr.ScanLocal(),
		filtered:         all,
		cwd:              cwd,
		manager:          mgr,
		notice:           notice,
		theme:            theme,
		repos:            repos,
		loading:          loading,
		directory:        directory,
	}
	form.applyFilter()
	if loading {
		return form, loadSkillsCmd(mgr, repos, directory)
	}
	return form, nil
}

func loadSkillsCmd(mgr *skills.Manager, repos []string, directory bool) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var found []skills.Skill
		var err error
		if directory {
			found, err = mgr.RefreshDirectoryCache(ctx)
		} else {
			found, err = mgr.RefreshCache(ctx, repos)
		}
		return skillsLoadedMsg{found: found, err: err, repos: repos}
	}
}

func (f skillsForm) Update(msg tea.Msg) (skillsForm, tea.Cmd) {
	switch msg := msg.(type) {
	case skillsLoadedMsg:
		selected := f.selectedName()
		f.loading = false
		f.installedResults = f.manager.ScanLocal()
		if msg.err != nil {
			f.notice = "Skills CLI unavailable: " + msg.err.Error() + "; showing offline fallback"
			if len(f.allResults) == 0 {
				f.allResults = fallbackSkills(f.manager)
			}
		} else {
			f.notice = "source: Skills CLI cache updated"
			f.allResults = mergeListedWithLocal(msg.found, f.installedResults)
		}
		f.applyFilter()
		f.selectName(selected)
		return f, nil
	case tea.KeyMsg:
		if f.confirmRemove != "" {
			switch msg.Type {
			case tea.KeyEsc:
				f.notice = "remove canceled"
				f.confirmRemove = ""
				return f, nil
			case tea.KeyEnter:
				removed, err := f.manager.RemoveInstalled(f.confirmRemove)
				if err != nil {
					f.errMsg = err.Error()
					f.confirmRemove = ""
					return f, nil
				}
				f.removed = removed.Name
				f.errMsg = ""
				f.confirmRemove = ""
				f.notice = "removed: " + removed.Name
				f.refreshInstalled()
				f.markAvailableRemoved(removed.Name)
				f.applyFilter()
				return f, nil
			default:
				return f, nil
			}
		}
		switch msg.Type {
		case tea.KeyEsc:
			f.canceled = true
			f.done = true
			return f, nil
		case tea.KeyLeft:
			f.setTab(skillsTabAvailable)
			return f, nil
		case tea.KeyRight:
			f.setTab(skillsTabInstalled)
			return f, nil
		case tea.KeyUp:
			if f.selected > 0 {
				f.selected--
			}
			f.ensureSelectedVisible()
			return f, nil
		case tea.KeyDown:
			if f.selected < len(f.filtered)-1 {
				f.selected++
			}
			f.ensureSelectedVisible()
			return f, nil
		case tea.KeyPgUp:
			f.selected = max(0, f.selected-skillsVisibleRows)
			f.ensureSelectedVisible()
			return f, nil
		case tea.KeyPgDown:
			f.selected = minInt(len(f.filtered)-1, f.selected+skillsVisibleRows)
			f.ensureSelectedVisible()
			return f, nil
		case tea.KeyHome:
			f.selected = 0
			f.ensureSelectedVisible()
			return f, nil
		case tea.KeyEnd:
			f.selected = max(0, len(f.filtered)-1)
			f.ensureSelectedVisible()
			return f, nil
		case tea.KeyCtrlR:
			f.loading = true
			f.errMsg = ""
			f.notice = "refreshing Skills CLI cache"
			return f, loadSkillsCmd(f.manager, f.repos, f.directory)
		case tea.KeyDelete, tea.KeyBackspace:
			if f.tab != skillsTabInstalled || len(f.filtered) == 0 || f.selected >= len(f.filtered) {
				return f, nil
			}
			skill := f.filtered[f.selected]
			if !canRemoveSkill(skill) {
				f.notice = "global install is read-only from Forge"
				return f, nil
			}
			f.confirmRemove = skill.Name
			f.notice = "Remove " + skill.Name + "? Enter confirm  Esc cancel"
			return f, nil
		case tea.KeyEnter:
			if f.tab == skillsTabInstalled {
				if selected := f.selectedSkill(); selected.Name != "" {
					f.notice = selected.Name + installPathLabel(selected.InstallPath)
				}
				return f, nil
			}
			if f.loading {
				f.notice = "skills are still loading; wait or press Esc to close"
				return f, nil
			}
			if len(f.filtered) > 0 && f.selected < len(f.filtered) {
				skill := f.filtered[f.selected]
				if skill.Installed {
					f.notice = skill.Name + " is already installed at " + skill.InstallPath
					return f, nil
				}
				var err error
				var installed skills.Skill
				if usesBuiltinSkillInstaller(skill) {
					err = skills.InstallBuiltin(f.cwd, skill.Name)
					if err != nil {
						err = fmt.Errorf("built-in skill install failed: %w", err)
					} else {
						installed, _ = f.manager.FindInstalled(skill.Name)
					}
				} else {
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					installed, err = f.manager.InstallAndVerify(ctx, skill)
					cancel()
					if err != nil {
						err = fmt.Errorf("skills CLI install failed: %w", err)
					}
				}
				if err != nil {
					f.errMsg = err.Error()
				} else {
					f.installed = skill.Name
					f.errMsg = ""
					f.notice = "installed: " + skill.Name + installPathLabel(installed.InstallPath)
					f.refreshInstalled()
					f.markInstalled(skill.Name, installed)
					f.applyFilter()
					f.selectName(skill.Name)
				}
			}
			return f, nil
		}
	}

	var cmd tea.Cmd
	f.search, cmd = f.search.Update(msg)

	// Filter by search term.
	f.applyFilter()
	return f, cmd
}

func (f *skillsForm) applyFilter() {
	query := strings.ToLower(strings.TrimSpace(f.search.Value()))
	source := f.allResults
	if f.tab == skillsTabInstalled {
		source = f.installedResults
	}
	if query == "" {
		f.filtered = source
	} else {
		var filtered []skills.Skill
		for _, s := range source {
			if strings.Contains(strings.ToLower(s.Name), query) ||
				strings.Contains(strings.ToLower(s.Description), query) ||
				strings.Contains(strings.ToLower(s.Source), query) ||
				strings.Contains(strings.ToLower(s.InstallPath), query) {
				filtered = append(filtered, s)
			}
		}
		f.filtered = filtered
	}
	if f.selected >= len(f.filtered) {
		f.selected = max(0, len(f.filtered)-1)
	}
	f.ensureSelectedVisible()
}

func (f skillsForm) View() string {
	t := f.theme
	box := lipgloss.NewStyle().
		Border(asciiBorder()).
		BorderForeground(lipgloss.Color("#af87d7")).
		Padding(1, 2).
		Width(92)

	content := t.TableHeader.Render("Skills Installer") + "\n"
	content += "  " + renderSkillTab(t, "Available", f.tab == skillsTabAvailable) + "  " + renderSkillTab(t, "Installed", f.tab == skillsTabInstalled) + "\n\n"
	if f.notice != "" {
		content += t.Muted.Render("  "+f.notice) + "\n\n"
	}
	if f.errMsg != "" {
		content += t.ErrorStyle.Render("  "+f.errMsg) + "\n\n"
	}
	content += f.search.View() + "\n\n"

	if len(f.filtered) == 0 {
		if f.tab == skillsTabInstalled {
			content += t.Muted.Render("  No installed skills found.")
		} else {
			content += t.Muted.Render("  No skills found. Use /skills refresh after checking network.")
		}
	} else {
		start, end := f.visibleRange()
		if f.tab == skillsTabInstalled {
			content += "  " + t.Muted.Render(pad("Skill", 24)+"  "+pad("Scope", 11)+"  "+pad("Path", 28)+"  Description") + "\n"
		} else {
			content += "  " + t.Muted.Render(pad("Skill", 24)+"  "+pad("State", 11)+"  "+pad("Repo", 28)+"  Description") + "\n"
		}
		for i := start; i < end; i++ {
			s := f.filtered[i]
			marker := "  "
			if i == f.selected {
				marker = t.IndicatorAgent.Render("> ")
			}
			line := f.renderSkillRow(s)
			content += marker + t.StatusValue.Render(line) + "\n"
		}
		if len(f.filtered) > skillsVisibleRows {
			content += t.Muted.Render("  Showing "+rangeLabel(start, end, len(f.filtered))+". PgUp/PgDown scroll.") + "\n"
		}
		if selected := f.selectedSkill(); selected.Name != "" {
			content += "\n" + t.Muted.Render("  Selected: "+selected.Name+installPathLabel(selected.InstallPath))
		}
	}
	if f.loading {
		content += "\n" + t.Muted.Render("  Fetching in background. Cached results will be reused next time.")
	}
	content += "\n" + t.Muted.Render("  Left/Right tabs  Up/Down navigate  PgUp/PgDown scroll  Delete remove  Enter install  Ctrl+R refresh  Esc close")
	return box.Render(content)
}

func (f skillsForm) Result() string {
	t := f.theme
	if f.canceled {
		return ""
	}
	if f.errMsg != "" {
		return t.ErrorStyle.Render("  Install failed: " + f.errMsg)
	}
	if f.installed != "" {
		return t.Success.Render("  Installed: " + f.installed)
	}
	if f.removed != "" {
		return t.Success.Render("  Removed: " + f.removed)
	}
	return ""
}

func fallbackSkills(mgr *skills.Manager) []skills.Skill {
	local := mgr.ScanLocal()
	installed := map[string]bool{}
	for _, s := range local {
		installed[s.Name] = true
	}
	all := local
	all = append(all, skills.SearchAvailable("", installed)...)
	return all
}

func usesBuiltinSkillInstaller(skill skills.Skill) bool {
	source := strings.TrimSpace(skill.Source)
	if source == "builtin" {
		return true
	}
	return source == "" && strings.TrimSpace(skill.Repo) == ""
}

const skillsVisibleRows = 12

func normalizeSkillRepos(mgr *skills.Manager, repos []string) []string {
	if len(repos) > 0 {
		return repos
	}
	return nil
}

func cachedSkills(mgr *skills.Manager, repos []string, directory bool) ([]skills.Skill, bool) {
	if directory {
		return mgr.ListDirectoryCached()
	}
	return mgr.ListCached(repos)
}

func cacheNotice(mgr *skills.Manager, repos []string, directory bool) string {
	if directory {
		info := mgr.DirectoryCacheInfo()
		if info.Error != "" {
			return "skills.sh cache error: " + info.Error
		}
		if !info.Exists {
			return "skills.sh directory has no cache"
		}
		return "skills.sh cached " + info.UpdatedAt.Format("2006-01-02 15:04") + " (" + strconv.Itoa(info.Count) + ")"
	}
	var parts []string
	for _, info := range mgr.CacheInfo(repos) {
		if info.Error != "" {
			parts = append(parts, info.Repo+" cache error")
			continue
		}
		if !info.Exists {
			parts = append(parts, info.Repo+" no cache")
			continue
		}
		parts = append(parts, info.Repo+" cached "+info.UpdatedAt.Format("2006-01-02 15:04")+" ("+strconv.Itoa(info.Count)+")")
	}
	if len(parts) == 0 {
		return "source: cache"
	}
	return "cache: " + strings.Join(parts, " | ")
}

func mergeListedWithLocal(listed, local []skills.Skill) []skills.Skill {
	all := append([]skills.Skill(nil), listed...)
	seen := map[string]bool{}
	for _, s := range all {
		seen[s.Name] = true
	}
	for _, s := range local {
		if !seen[s.Name] {
			all = append(all, s)
		}
	}
	return all
}

func (f *skillsForm) ensureSelectedVisible() {
	if f.selected < 0 {
		f.selected = 0
	}
	if f.offset < 0 {
		f.offset = 0
	}
	if f.selected < f.offset {
		f.offset = f.selected
	}
	if f.selected >= f.offset+skillsVisibleRows {
		f.offset = f.selected - skillsVisibleRows + 1
	}
	maxOffset := max(0, len(f.filtered)-skillsVisibleRows)
	if f.offset > maxOffset {
		f.offset = maxOffset
	}
}

func (f *skillsForm) setTab(tab skillsTab) {
	if f.tab == tab {
		return
	}
	f.tab = tab
	f.selected = 0
	f.offset = 0
	f.errMsg = ""
	f.confirmRemove = ""
	f.applyFilter()
}

func (f *skillsForm) refreshInstalled() {
	f.installedResults = f.manager.ScanLocal()
}

func (f skillsForm) selectedName() string {
	if f.selected >= 0 && f.selected < len(f.filtered) {
		return f.filtered[f.selected].Name
	}
	return ""
}

func (f skillsForm) selectedSkill() skills.Skill {
	if f.selected >= 0 && f.selected < len(f.filtered) {
		return f.filtered[f.selected]
	}
	return skills.Skill{}
}

func (f *skillsForm) selectName(name string) {
	if name == "" {
		return
	}
	for i, skill := range f.filtered {
		if skill.Name == name {
			f.selected = i
			f.ensureSelectedVisible()
			return
		}
	}
}

func (f *skillsForm) markInstalled(name string, installed skills.Skill) {
	for i := range f.allResults {
		if f.allResults[i].Name == name {
			f.allResults[i].Installed = true
			f.allResults[i].InstallPath = installed.InstallPath
		}
	}
}

func (f *skillsForm) markAvailableRemoved(name string) {
	for i := range f.allResults {
		if f.allResults[i].Name == name {
			f.allResults[i].Installed = false
			f.allResults[i].InstallPath = ""
		}
	}
}

func (f skillsForm) renderSkillRow(s skills.Skill) string {
	if f.tab == skillsTabInstalled {
		return pad(truncateASCII(s.Name, 24), 24) + "  " +
			pad(truncateASCII(s.Source, 11), 11) + "  " +
			pad(truncateASCII(s.InstallPath, 28), 28) + "  " +
			truncateASCII(s.Description, 42)
	}
	state := "available"
	if s.Installed {
		state = "installed"
	} else if s.Source == "builtin" {
		state = "offline"
	}
	repo := s.Repo
	if repo == "" {
		repo = s.Source
	}
	return pad(truncateASCII(s.Name, 24), 24) + "  " +
		pad(truncateASCII(state, 11), 11) + "  " +
		pad(truncateASCII(repo, 28), 28) + "  " +
		truncateASCII(s.Description, 42)
}

func renderSkillTab(t Theme, label string, active bool) string {
	if active {
		return t.StatusValue.Render("[" + label + "]")
	}
	return t.Muted.Render(" " + label + " ")
}

func canRemoveSkill(skill skills.Skill) bool {
	return skill.Source == "project" || skill.Source == "legacy"
}

func (f skillsForm) visibleRange() (int, int) {
	start := minInt(f.offset, max(0, len(f.filtered)-1))
	end := minInt(len(f.filtered), start+skillsVisibleRows)
	return start, end
}

func rangeLabel(start, end, total int) string {
	return strconv.Itoa(start+1) + "-" + strconv.Itoa(end) + " of " + strconv.Itoa(total)
}

func truncateASCII(s string, width int) string {
	if len(s) <= width {
		return s
	}
	if width <= 3 {
		return s[:width]
	}
	return s[:width-3] + "..."
}

func installPathLabel(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	return " -> " + path
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func asciiBorder() lipgloss.Border {
	return lipgloss.Border{
		Top:         "-",
		Bottom:      "-",
		Left:        "|",
		Right:       "|",
		TopLeft:     "+",
		TopRight:    "+",
		BottomLeft:  "+",
		BottomRight: "+",
	}
}
