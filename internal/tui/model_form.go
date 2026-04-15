package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"forge/internal/config"
	"forge/internal/llm"

	"github.com/pelletier/go-toml/v2"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type modelFormStep int

const (
	stepSelectModel modelFormStep = iota
	stepSelectContext
	stepLoading
)

type ctxOption struct {
	label string
	ctx   int // 0 means "keep current, no reload"
}

type modelForm struct {
	step  modelFormStep
	role  string
	title string

	// step 1 — model picker
	models     []string
	modelInfos map[string]llm.ModelInfo
	selected   int
	current    string

	// step 2 — context picker
	chosenModel string
	ctxOptions  []ctxOption
	ctxCursor   int

	// step 3 — loading + result
	loadErr     error
	loadedModel string
	loadedCtx   int
	detected    *config.DetectedContext

	done     bool
	canceled bool
	chosen   string
	errMsg   string

	cwd      string
	theme    Theme
	provider llm.Provider
	profile  config.YarnProfile
}

// loadCompleteMsg is dispatched from the background load goroutine.
type loadCompleteMsg struct {
	modelID  string
	detected *config.DetectedContext
	err      error
}

func newModelForm(cwd string, cfg config.Config, providers *llm.Registry, theme Theme) modelForm {
	return newModelFormForRole(cwd, cfg, providers, theme, "chat", "Model Selector")
}

func newModelFormForRole(cwd string, cfg config.Config, providers *llm.Registry, theme Theme, role, title string) modelForm {
	providerName := cfg.Providers.Default.Name
	if providerName == "" {
		providerName = "lmstudio"
	}

	if role == "" {
		role = "chat"
	}
	if title == "" {
		title = "Model Selector"
	}
	current := cfg.Models[role]
	if current == "" && role == "explorer" {
		current = cfg.Models["planner"]
	}
	if current == "" {
		current = cfg.Models["chat"]
	}
	f := modelForm{
		current:    current,
		role:       role,
		title:      title,
		cwd:        cwd,
		theme:      theme,
		step:       stepSelectModel,
		modelInfos: map[string]llm.ModelInfo{},
	}
	if profile, ok := config.GetYarnProfile(cfg.Context.Yarn.Profile); ok {
		f.profile = profile
	}

	provider, ok := providers.Get(providerName)
	if !ok {
		f.errMsg = "Provider " + providerName + " not registered."
		return f
	}
	f.provider = provider

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	models, err := provider.ListModels(ctx)
	if err != nil {
		f.errMsg = "Failed to list models: " + err.Error()
		return f
	}
	if len(models) == 0 {
		f.errMsg = "No models available from " + providerName + "."
		return f
	}

	for i, m := range models {
		f.models = append(f.models, m.ID)
		f.modelInfos[m.ID] = m
		if m.ID == current {
			f.selected = i
		}
	}
	return f
}

func (f modelForm) Update(msg tea.Msg) (modelForm, tea.Cmd) {
	if done, ok := msg.(loadCompleteMsg); ok {
		f.loadErr = done.err
		f.detected = done.detected
		f.loadedModel = done.modelID
		f.done = true
		if done.err == nil {
			f.chosen = done.modelID
		}
		return f, nil
	}
	keyMsg, isKey := msg.(tea.KeyMsg)
	if !isKey {
		return f, nil
	}

	switch keyMsg.Type {
	case tea.KeyEsc:
		if f.step == stepLoading {
			// Don't cancel a load in flight — let the goroutine finish.
			return f, nil
		}
		f.canceled = true
		f.done = true
		return f, nil
	}

	switch f.step {
	case stepSelectModel:
		return f.updateModelStep(keyMsg)
	case stepSelectContext:
		return f.updateContextStep(keyMsg)
	case stepLoading:
		return f, nil
	}
	return f, nil
}

func (f modelForm) updateModelStep(msg tea.KeyMsg) (modelForm, tea.Cmd) {
	switch msg.Type {
	case tea.KeyUp:
		if f.selected > 0 {
			f.selected--
		}
	case tea.KeyDown:
		if f.selected < len(f.models)-1 {
			f.selected++
		}
	case tea.KeyEnter:
		if f.errMsg != "" || len(f.models) == 0 {
			f.done = true
			f.canceled = true
			return f, nil
		}
		f.chosenModel = f.models[f.selected]
		info := f.modelInfos[f.chosenModel]
		f.ctxOptions = buildContextOptions(info, f.profile)
		f.ctxCursor = 0
		f.step = stepSelectContext
	}
	return f, nil
}

func (f modelForm) updateContextStep(msg tea.KeyMsg) (modelForm, tea.Cmd) {
	switch msg.Type {
	case tea.KeyUp:
		if f.ctxCursor > 0 {
			f.ctxCursor--
		}
	case tea.KeyDown:
		if f.ctxCursor < len(f.ctxOptions)-1 {
			f.ctxCursor++
		}
	case tea.KeyEnter:
		if len(f.ctxOptions) == 0 {
			f.done = true
			return f, nil
		}
		chosen := f.ctxOptions[f.ctxCursor]
		f.loadedCtx = chosen.ctx
		if chosen.ctx <= 0 {
			// "Keep current" — skip load.
			f.chosen = f.chosenModel
			f.done = true
			return f, nil
		}
		f.step = stepLoading
		return f, loadModelCmd(f.provider, f.chosenModel, chosen.ctx)
	}
	return f, nil
}

// loadModelCmd runs the load in the background and reports the probed state.
func loadModelCmd(provider llm.Provider, modelID string, contextLength int) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
		defer cancel()
		err := provider.LoadModel(ctx, modelID, llm.LoadConfig{
			ContextLength:  contextLength,
			FlashAttention: true,
		})
		if err != nil {
			if errors.Is(err, llm.ErrNotSupported) {
				return loadCompleteMsg{modelID: modelID, err: fmt.Errorf("provider does not support programmatic model loading")}
			}
			return loadCompleteMsg{modelID: modelID, err: err}
		}
		// Reprobe to capture the actual loaded_context_length.
		probeCtx, probeCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer probeCancel()
		info, perr := provider.ProbeModel(probeCtx, modelID)
		if perr != nil || info == nil || info.LoadedContextLength <= 0 {
			// Load succeeded but probe did not report back — still report success.
			return loadCompleteMsg{modelID: modelID, detected: &config.DetectedContext{
				ModelID:             modelID,
				LoadedContextLength: contextLength,
				ProbedAt:            time.Now().UTC(),
			}}
		}
		return loadCompleteMsg{
			modelID: modelID,
			detected: &config.DetectedContext{
				ModelID:             info.ID,
				LoadedContextLength: info.LoadedContextLength,
				MaxContextLength:    info.MaxContextLength,
				ProbedAt:            time.Now().UTC(),
			},
		}
	}
}

// buildContextOptions enumerates context-length choices for the context
// picker. Dedupes and sorts so users never see the same number twice.
func buildContextOptions(info llm.ModelInfo, profile config.YarnProfile) []ctxOption {
	seen := map[int]bool{}
	var options []ctxOption
	add := func(label string, ctx int) {
		if ctx <= 0 || seen[ctx] {
			return
		}
		seen[ctx] = true
		options = append(options, ctxOption{label: label, ctx: ctx})
	}
	options = append(options, ctxOption{label: "Keep current (no reload)", ctx: 0})
	if info.LoadedContextLength > 0 {
		add(fmt.Sprintf("Current loaded (%dk)", info.LoadedContextLength/1024), info.LoadedContextLength)
	}
	if profile.LMContextMax > 0 {
		add(fmt.Sprintf("Profile %s recommended (%dk)", profile.Name, profile.LMContextMax/1024), profile.LMContextMax)
	}
	if info.MaxContextLength > 0 {
		add(fmt.Sprintf("Model max (%dk)", info.MaxContextLength/1024), info.MaxContextLength)
	}
	// Useful round-number YaRN targets for models without reported max.
	for _, target := range []int{32768, 65536, 131072, 262144} {
		if info.MaxContextLength == 0 || target <= info.MaxContextLength {
			add(fmt.Sprintf("%dk", target/1024), target)
		}
	}
	return options
}

func (f modelForm) View() string {
	t := f.theme
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#af87d7")).
		Padding(1, 2).
		Width(64)

	_ = t
	switch f.step {
	case stepSelectContext:
		return box.Render(f.viewContextStep())
	case stepLoading:
		return box.Render(f.viewLoading())
	default:
		return box.Render(f.viewModelStep())
	}
}

func (f modelForm) viewModelStep() string {
	t := f.theme
	title := f.title
	if title == "" {
		title = "Model Selector"
	}
	content := t.TableHeader.Render(title) + "\n\n"

	if f.errMsg != "" {
		content += t.ErrorStyle.Render("  "+f.errMsg) + "\n"
		content += "\n" + t.Muted.Render("  Enter/Esc to close")
		return content
	}

	if len(f.models) == 0 {
		content += t.Muted.Render("  No models found.")
	} else {
		limit := len(f.models)
		if limit > 15 {
			limit = 15
		}
		offset := 0
		if f.selected >= limit {
			offset = f.selected - limit + 1
		}
		for i := offset; i < offset+limit && i < len(f.models); i++ {
			id := f.models[i]
			info := f.modelInfos[id]
			marker := "  "
			if i == f.selected {
				marker = t.IndicatorAgent.Render("> ")
			}
			label := t.StatusValue.Render(id)
			if id == f.current {
				label += t.Success.Render(" *")
			}
			if info.LoadedContextLength > 0 {
				label += t.Muted.Render(fmt.Sprintf("  (loaded %dk)", info.LoadedContextLength/1024))
			} else if info.State == "loaded" {
				label += t.Muted.Render("  (loaded)")
			}
			content += marker + label + "\n"
		}
		if len(f.models) > limit {
			content += t.Muted.Render("  ... and more. Up/Down to scroll.")
		}
	}

	content += "\n" + t.Muted.Render("  Up/Down  Enter: pick model → choose context  Esc: cancel")
	return content
}

func (f modelForm) viewContextStep() string {
	t := f.theme
	content := t.TableHeader.Render("Context Length") + "\n"
	content += t.Muted.Render("  Model: ") + t.StatusValue.Render(f.chosenModel) + "\n\n"
	for i, opt := range f.ctxOptions {
		marker := "  "
		if i == f.ctxCursor {
			marker = t.IndicatorAgent.Render("> ")
		}
		content += marker + t.StatusValue.Render(opt.label) + "\n"
	}
	content += "\n" + t.Muted.Render("  YaRN applies automatically for GGUFs with embedded rope scaling.")
	content += "\n" + t.Muted.Render("  Up/Down  Enter: load  Esc: cancel")
	return content
}

func (f modelForm) viewLoading() string {
	t := f.theme
	content := t.TableHeader.Render("Loading model...") + "\n\n"
	content += t.StatusValue.Render("  "+f.chosenModel) + t.Muted.Render(fmt.Sprintf("  ctx=%d", f.loadedCtx)) + "\n"
	content += "\n" + t.Muted.Render("  This may take a minute for large models. Do not close.")
	return content
}

// Apply commits the form outcome to config and returns a status message.
// Called by forms.go after f.done becomes true.
func (f modelForm) Apply(cfg *config.Config) string {
	return f.ApplyRole(cfg, f.role)
}

func (f modelForm) ApplyRole(cfg *config.Config, role string) string {
	return f.applyRole(cfg, role, true)
}

func (f modelForm) ApplyRoleInMemory(cfg *config.Config, role string) string {
	return f.applyRole(cfg, role, false)
}

func (f modelForm) applyRole(cfg *config.Config, role string, persist bool) string {
	t := f.theme
	if f.canceled {
		return ""
	}
	if f.loadErr != nil {
		return t.ErrorStyle.Render("Load failed: " + f.loadErr.Error())
	}
	if f.chosen == "" {
		return ""
	}
	if role == "" {
		role = "chat"
	}
	if cfg.Models == nil {
		cfg.Models = map[string]string{}
	}
	cfg.Models[role] = f.chosen
	if f.detected != nil && f.detected.LoadedContextLength > 0 {
		if role == "chat" {
			cfg.Context.Detected = f.detected
		}
		config.SetDetectedForRole(cfg, role, f.detected)
	} else {
		// Model changed without a new load — invalidate stale detection.
		if role == "chat" && cfg.Context.Detected != nil && cfg.Context.Detected.ModelID != f.chosen {
			cfg.Context.Detected = nil
		}
	}
	if persist {
		f.persistConfig(*cfg)
	}
	label := role
	if label == "chat" {
		label = "model"
	}
	msg := t.Success.Render(label + " set to: " + f.chosen)
	if f.detected != nil && f.detected.LoadedContextLength > 0 {
		msg += fmt.Sprintf("\nLoaded with context length %d", f.detected.LoadedContextLength)
		_, budget, reserve := config.EffectiveBudgets(*cfg)
		msg += t.Muted.Render(fmt.Sprintf("\nYARN budget=%d reserve=%d", budget, reserve))
	} else {
		msg += t.Muted.Render("\nRun /yarn probe to re-detect context window.")
	}
	return msg
}

func (f modelForm) persistConfig(cfg config.Config) {
	dir := filepath.Join(f.cwd, ".forge")
	_ = os.MkdirAll(dir, 0o755)
	data, err := toml.Marshal(cfg)
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, "config.toml"), data, 0o644)
}
