package tui

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"forge/internal/config"
	"forge/internal/llm"

	"github.com/pelletier/go-toml/v2"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type modelForm struct {
	models   []string
	selected int
	current  string
	done     bool
	canceled bool
	chosen   string
	errMsg   string
	cwd      string
	theme    Theme
}

func newModelForm(cwd string, cfg config.Config, providers *llm.Registry, theme Theme) modelForm {
	providerName := cfg.Providers.Default.Name
	if providerName == "" {
		providerName = "lmstudio"
	}

	current := cfg.Models["chat"]
	f := modelForm{
		current: current,
		cwd:     cwd,
		theme:   theme,
	}

	provider, ok := providers.Get(providerName)
	if !ok {
		f.errMsg = "Provider " + providerName + " not registered."
		return f
	}

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
		if m.ID == current {
			f.selected = i
		}
	}
	return f
}

func (f modelForm) Update(msg tea.Msg) (modelForm, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEsc:
			f.canceled = true
			f.done = true
			return f, nil
		case tea.KeyUp:
			if f.selected > 0 {
				f.selected--
			}
			return f, nil
		case tea.KeyDown:
			if f.selected < len(f.models)-1 {
				f.selected++
			}
			return f, nil
		case tea.KeyEnter:
			if len(f.models) > 0 && f.selected < len(f.models) {
				f.chosen = f.models[f.selected]
				f.done = true
			} else if f.errMsg != "" {
				f.done = true
				f.canceled = true
			}
			return f, nil
		}
	}
	return f, nil
}

func (f modelForm) View() string {
	t := f.theme
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#af87d7")).
		Padding(1, 2).
		Width(64)

	content := t.TableHeader.Render("Model Selector") + "\n\n"

	if f.errMsg != "" {
		content += t.ErrorStyle.Render("  "+f.errMsg) + "\n"
		content += "\n" + t.Muted.Render("  Enter/Esc to close")
		return box.Render(content)
	}

	if len(f.models) == 0 {
		content += t.Muted.Render("  No models found.")
	} else {
		limit := len(f.models)
		if limit > 15 {
			limit = 15
		}
		// Calculate scroll offset to keep selected visible.
		offset := 0
		if f.selected >= limit {
			offset = f.selected - limit + 1
		}
		for i := offset; i < offset+limit && i < len(f.models); i++ {
			id := f.models[i]
			marker := "  "
			if i == f.selected {
				marker = t.IndicatorAgent.Render("> ")
			}
			label := t.StatusValue.Render(id)
			if id == f.current {
				label += t.Success.Render(" *")
			}
			content += marker + label + "\n"
		}
		if len(f.models) > limit {
			content += t.Muted.Render("  ... and more. Up/Down to scroll.")
		}
	}

	content += "\n" + t.Muted.Render("  Up/Down navigate  Enter select  Esc cancel")
	return box.Render(content)
}

// Apply sets the chosen model in the config, persists to disk, and returns a status message.
func (f modelForm) Apply(cfg *config.Config) string {
	t := f.theme
	if f.canceled || f.chosen == "" {
		return ""
	}
	cfg.Models["chat"] = f.chosen
	f.persistConfig(*cfg)
	return t.Success.Render("Model set to: " + f.chosen)
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
