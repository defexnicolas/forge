package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"forge/internal/config"
	"forge/internal/llm"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/pelletier/go-toml/v2"
)

type modelMultiStep int

const (
	stepModelMultiStrategy modelMultiStep = iota
	stepModelMultiRole
)

type modelMultiRole struct {
	label string
	role  string
}

type modelMultiSelection struct {
	role     string
	modelID  string
	detected *config.DetectedContext
}

type modelMultiForm struct {
	step           modelMultiStep
	strategyCursor int
	strategy       string
	roleIdx        int
	roleForm       modelForm
	roles          []modelMultiRole
	results        []string
	selections     []modelMultiSelection
	cfg            config.Config
	cwd            string
	theme          Theme
	providers      *llm.Registry
	done           bool
	canceled       bool
	errMsg         string
}

func newModelMultiForm(cwd string, cfg config.Config, providers *llm.Registry, theme Theme) modelMultiForm {
	config.Normalize(&cfg)
	cursor := 0
	if strings.EqualFold(cfg.ModelLoading.Strategy, "parallel") {
		cursor = 1
	}
	return modelMultiForm{
		step:           stepModelMultiStrategy,
		strategyCursor: cursor,
		strategy:       cfg.ModelLoading.Strategy,
		cfg:            cfg,
		cwd:            cwd,
		theme:          theme,
		providers:      providers,
		roles: []modelMultiRole{
			{label: "EXPLORER", role: "explorer"},
			{label: "PLAN", role: "planner"},
			{label: "BUILDER", role: "editor"},
			{label: "REVIEWER", role: "reviewer"},
			{label: "SUMMARIZER", role: "summarizer"},
		},
	}
}

func (f modelMultiForm) Update(msg tea.Msg) (modelMultiForm, tea.Cmd) {
	keyMsg, isKey := msg.(tea.KeyMsg)
	if isKey && keyMsg.Type == tea.KeyEsc {
		if f.step == stepModelMultiRole && f.roleForm.step == stepLoading {
			return f, nil
		}
		f.canceled = true
		f.done = true
		return f, nil
	}

	switch f.step {
	case stepModelMultiStrategy:
		if !isKey {
			return f, nil
		}
		return f.updateStrategyStep(keyMsg)
	case stepModelMultiRole:
		var cmd tea.Cmd
		f.roleForm, cmd = f.roleForm.Update(msg)
		if f.roleForm.done {
			if f.roleForm.canceled {
				f.canceled = true
				f.done = true
				return f, nil
			}
			role := f.roles[f.roleIdx]
			result := f.roleForm.ApplyRoleInMemory(&f.cfg, role.role)
			if result != "" {
				f.results = append(f.results, result)
			}
			if f.roleForm.loadErr != nil {
				f.errMsg = stripAnsi(result)
				f.done = true
				return f, nil
			}
			if f.roleForm.chosen != "" {
				f.selections = append(f.selections, modelMultiSelection{
					role:     role.role,
					modelID:  f.roleForm.chosen,
					detected: f.roleForm.detected,
				})
			}
			f.roleIdx++
			if f.roleIdx >= len(f.roles) {
				f.cfg.ModelLoading.Enabled = true
				f.cfg.ModelLoading.Strategy = f.strategy
				f.persistConfig(f.cfg)
				f.done = true
				return f, nil
			}
			f.roleForm = f.newRoleForm()
			return f, nil
		}
		return f, cmd
	}
	return f, nil
}

func (f modelMultiForm) updateStrategyStep(msg tea.KeyMsg) (modelMultiForm, tea.Cmd) {
	switch msg.Type {
	case tea.KeyUp, tea.KeyDown:
		if f.strategyCursor == 0 {
			f.strategyCursor = 1
		} else {
			f.strategyCursor = 0
		}
	case tea.KeyEnter:
		f.strategy = "single"
		if f.strategyCursor == 1 {
			f.strategy = "parallel"
		}
		f.cfg.ModelLoading.Enabled = true
		f.cfg.ModelLoading.Strategy = f.strategy
		f.step = stepModelMultiRole
		f.roleIdx = 0
		f.roleForm = f.newRoleForm()
	}
	return f, nil
}

func (f modelMultiForm) newRoleForm() modelForm {
	role := f.roles[f.roleIdx]
	return newModelFormForRole(
		f.cwd,
		f.cfg,
		f.providers,
		f.theme,
		role.role,
		fmt.Sprintf("Que modelo usaras para %s?", role.label),
	)
}

func (f modelMultiForm) View() string {
	t := f.theme
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#af87d7")).
		Padding(1, 2).
		Width(72)
	if f.errMsg != "" {
		return box.Render(t.TableHeader.Render("Model Multi") + "\n\n" + t.ErrorStyle.Render(f.errMsg) + "\n\n" + t.Muted.Render("Enter/Esc to close"))
	}
	if f.step == stepModelMultiRole {
		return f.roleForm.View()
	}
	content := t.TableHeader.Render("Model Multi") + "\n\n"
	content += t.Muted.Render("Como quieres cargar los modelos del provider?") + "\n\n"
	options := []struct {
		key  string
		text string
	}{
		{key: "single", text: "Menor VRAM; Forge carga/switchea el modelo antes de usar cada rol."},
		{key: "parallel", text: "Mantiene modelos por rol cargados si LM Studio lo permite; ideal para subagentes concurrentes."},
	}
	for i, opt := range options {
		marker := "  "
		if i == f.strategyCursor {
			marker = t.IndicatorAgent.Render("> ")
		}
		content += marker + t.StatusValue.Render(opt.key) + t.Muted.Render(" - "+opt.text) + "\n"
	}
	content += "\n" + t.Muted.Render("Up/Down  Enter: continue  Esc: cancel")
	return box.Render(content)
}

func (f modelMultiForm) Result() string {
	t := f.theme
	if f.canceled {
		return t.Muted.Render("Model multi canceled.")
	}
	if f.errMsg != "" {
		return t.ErrorStyle.Render(f.errMsg)
	}
	var b strings.Builder
	b.WriteString(t.Success.Render("Model multi configured"))
	b.WriteString(t.Muted.Render(" strategy=" + f.strategy))
	for _, result := range f.results {
		if result == "" {
			continue
		}
		b.WriteString("\n")
		b.WriteString(result)
	}
	return b.String()
}

func (f modelMultiForm) persistConfig(cfg config.Config) {
	dir := filepath.Join(f.cwd, ".forge")
	_ = os.MkdirAll(dir, 0o755)
	data, err := toml.Marshal(cfg)
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, "config.toml"), data, 0o644)
}
