package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"forge/internal/config"
	"forge/internal/llm"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/pelletier/go-toml/v2"
)

// contextWithShortTimeout returns a 5s context for the quick ListModels
// query that powers the reuse step. If the LM Studio endpoint hangs, we'd
// rather fall through to an empty reuse list than block the form.
func contextWithShortTimeout() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}

type modelMultiStep int

const (
	stepModelMultiStrategy modelMultiStep = iota
	stepModelMultiReuse
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

// modelReuseOption is a candidate model the user can reuse for the next role
// without triggering another LoadModel call — either already assigned to a
// prior role in this form session, or already resident in LM Studio.
type modelReuseOption struct {
	modelID  string
	detected *config.DetectedContext
	source   string // short label: "from EXPLORER" or "loaded in LM Studio"
	ctxLabel string // precomputed display like "8k ctx"
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
	// Reuse step state — rebuilt each time the form advances to a new role.
	reuseCursor  int
	reuseOptions []modelReuseOption
	// loadedModels caches provider.ListModels() output (filtered to loaded
	// only) so we don't re-hit LM Studio for every role. Populated lazily on
	// first reuse-step build.
	loadedModels      []llm.ModelInfo
	loadedModelsKnown bool
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
	case stepModelMultiReuse:
		if !isKey {
			return f, nil
		}
		return f.updateReuseStep(keyMsg)
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
			return f.advanceToNextRole(), nil
		}
		return f, cmd
	}
	return f, nil
}

// advanceToNextRole is called after the user finishes assigning a model to a
// role. It either terminates the form, or sets up the next role's UI —
// preferring the reuse step when there are already-loaded candidates so the
// user doesn't end up loading the same model twice.
func (f modelMultiForm) advanceToNextRole() modelMultiForm {
	if f.roleIdx >= len(f.roles) {
		f.cfg.ModelLoading.Enabled = true
		f.cfg.ModelLoading.Strategy = f.strategy
		f.persistConfig(f.cfg)
		f.done = true
		return f
	}
	f.reuseOptions = f.buildReuseOptions()
	if len(f.reuseOptions) > 0 {
		f.step = stepModelMultiReuse
		f.reuseCursor = 0
		return f
	}
	f.step = stepModelMultiRole
	f.roleForm = f.newRoleForm()
	return f
}

// buildReuseOptions assembles the list of models the user can reuse for the
// current role without re-loading: (1) models already assigned in this
// form session, deduped by model ID; (2) models LM Studio reports as loaded
// externally. Reuse-by-selection wins over reuse-by-LM-Studio when a model
// appears in both.
func (f *modelMultiForm) buildReuseOptions() []modelReuseOption {
	opts := []modelReuseOption{}
	seen := map[string]bool{}
	// Session-level selections first — the user just picked these, the
	// labels tell them which role. detected is the LoadedContextLength
	// recorded after the successful LoadModel call.
	for _, sel := range f.selections {
		if sel.modelID == "" || seen[sel.modelID] {
			continue
		}
		label := "from " + strings.ToUpper(sel.role)
		opts = append(opts, modelReuseOption{
			modelID:  sel.modelID,
			detected: sel.detected,
			source:   label,
			ctxLabel: formatCtxLabel(sel.detected),
		})
		seen[sel.modelID] = true
	}
	// LM Studio externally-loaded models — populated lazily on first call.
	if !f.loadedModelsKnown {
		f.loadedModels = f.queryLoadedModels()
		f.loadedModelsKnown = true
	}
	for _, info := range f.loadedModels {
		if info.ID == "" || seen[info.ID] {
			continue
		}
		var detected *config.DetectedContext
		if info.LoadedContextLength > 0 {
			detected = &config.DetectedContext{
				ModelID:             info.ID,
				LoadedContextLength: info.LoadedContextLength,
				MaxContextLength:    info.MaxContextLength,
			}
		}
		opts = append(opts, modelReuseOption{
			modelID:  info.ID,
			detected: detected,
			source:   "loaded in LM Studio",
			ctxLabel: formatCtxLabel(detected),
		})
		seen[info.ID] = true
	}
	return opts
}

func (f *modelMultiForm) queryLoadedModels() []llm.ModelInfo {
	providerName := f.cfg.Providers.Default.Name
	if providerName == "" {
		providerName = "lmstudio"
	}
	provider, ok := f.providers.Get(providerName)
	if !ok {
		return nil
	}
	ctx, cancel := contextWithShortTimeout()
	defer cancel()
	models, err := provider.ListModels(ctx)
	if err != nil {
		return nil
	}
	var loaded []llm.ModelInfo
	for _, m := range models {
		// Only trust an explicit State="loaded" here. LoadedContextLength
		// alone is unreliable — some builds of LM Studio surface it for
		// previously-loaded-but-unloaded models too, which would offer
		// reuse for models that aren't actually resident right now.
		if m.State == "loaded" {
			loaded = append(loaded, m)
		}
	}
	return loaded
}

func formatCtxLabel(detected *config.DetectedContext) string {
	if detected == nil || detected.LoadedContextLength <= 0 {
		return ""
	}
	return fmt.Sprintf("%dk ctx", detected.LoadedContextLength/1024)
}

// updateReuseStep handles arrow-key navigation and selection in the reuse
// step. The last entry is always "Pick a different model" — selecting it
// falls through to the standard modelForm picker for the current role.
func (f modelMultiForm) updateReuseStep(msg tea.KeyMsg) (modelMultiForm, tea.Cmd) {
	total := len(f.reuseOptions) + 1 // +1 for "Pick a different model"
	switch msg.Type {
	case tea.KeyUp:
		if f.reuseCursor > 0 {
			f.reuseCursor--
		}
	case tea.KeyDown:
		if f.reuseCursor < total-1 {
			f.reuseCursor++
		}
	case tea.KeyEnter:
		if f.reuseCursor >= len(f.reuseOptions) {
			// "Pick a different model" — fall through to the standard
			// modelForm picker.
			f.step = stepModelMultiRole
			f.roleForm = f.newRoleForm()
			return f, nil
		}
		opt := f.reuseOptions[f.reuseCursor]
		role := f.roles[f.roleIdx]
		// Synthesize a minimal modelForm carrying the reused model + its
		// detected context, and feed it through the existing ApplyRole
		// path. This reuses all the per-role config wiring without
		// touching the (byte-stable) apply logic.
		proxy := modelForm{
			chosen:   opt.modelID,
			detected: opt.detected,
			theme:    f.theme,
			role:     role.role,
			cwd:      f.cwd,
		}
		result := proxy.ApplyRoleInMemory(&f.cfg, role.role)
		if result != "" {
			f.results = append(f.results, result)
		}
		f.selections = append(f.selections, modelMultiSelection{
			role:     role.role,
			modelID:  opt.modelID,
			detected: opt.detected,
		})
		f.roleIdx++
		return f.advanceToNextRole(), nil
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
		f.roleIdx = 0
		return f.advanceToNextRole(), nil
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
	if f.step == stepModelMultiReuse {
		return box.Render(f.viewReuseStep())
	}
	content := t.TableHeader.Render("Model Multi") + "\n\n"
	content += t.Muted.Render("Como quieres cargar los modelos del provider?") + "\n\n"
	options := []struct {
		key  string
		text string
	}{
		{key: "single", text: "Un modelo cargado; aun puede usar varios GEN slots para requests concurrentes."},
		{key: "parallel", text: "Mantiene modelos por rol cargados si LM Studio lo permite; la concurrencia depende de GEN slots."},
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

func (f modelMultiForm) viewReuseStep() string {
	t := f.theme
	role := f.roles[f.roleIdx]
	var b strings.Builder
	b.WriteString(t.TableHeader.Render("Model Multi -- Reuse?"))
	b.WriteString("\n\n")
	b.WriteString(t.Muted.Render(fmt.Sprintf("Role: %s (%d/%d)", role.label, f.roleIdx+1, len(f.roles))))
	b.WriteString("\n")
	b.WriteString(t.Muted.Render("Usar un modelo ya cargado para este rol, o seleccionar otro?"))
	b.WriteString("\n\n")
	for i, opt := range f.reuseOptions {
		marker := "  "
		if i == f.reuseCursor {
			marker = t.IndicatorAgent.Render("> ")
		}
		line := marker + t.StatusValue.Render(opt.modelID)
		suffix := "  (" + opt.source
		if opt.ctxLabel != "" {
			suffix += ", " + opt.ctxLabel
		}
		suffix += ")"
		b.WriteString(line + t.Muted.Render(suffix) + "\n")
	}
	marker := "  "
	if f.reuseCursor == len(f.reuseOptions) {
		marker = t.IndicatorAgent.Render("> ")
	}
	b.WriteString(marker + t.Muted.Render("Pick a different model ...") + "\n")
	b.WriteString("\n" + t.Muted.Render("Up/Down  Enter: select  Esc: cancel"))
	return b.String()
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
