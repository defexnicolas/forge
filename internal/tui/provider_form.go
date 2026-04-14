package tui

import (
	"os"
	"path/filepath"

	"forge/internal/config"
	"forge/internal/llm"

	"github.com/pelletier/go-toml/v2"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const (
	fieldURL = iota
	fieldKey
	fieldModel
	fieldCount
)

type providerForm struct {
	fields   [fieldCount]textinput.Model
	focused  int
	done     bool
	canceled bool
	cwd      string
	theme    Theme
}

func newProviderForm(cwd string, cfg config.Config, theme Theme) providerForm {
	providerCfg := cfg.Providers.LMStudio
	if cfg.Providers.Default.Name == "openai_compatible" {
		providerCfg = cfg.Providers.OpenAICompatible
	}

	fields := [fieldCount]textinput.Model{}

	url := textinput.New()
	url.Placeholder = "http://localhost:1234/v1"
	url.SetValue(providerCfg.BaseURL)
	url.Focus()
	url.Width = 50
	url.Prompt = "  URL   "
	fields[fieldURL] = url

	key := textinput.New()
	key.Placeholder = "lm-studio"
	key.SetValue(providerCfg.APIKey)
	key.Width = 50
	key.Prompt = "  Key   "
	fields[fieldKey] = key

	model := textinput.New()
	model.Placeholder = "local-model"
	model.SetValue(cfg.Models["chat"])
	model.Width = 50
	model.Prompt = "  Model "
	fields[fieldModel] = model

	return providerForm{
		fields:  fields,
		focused: fieldURL,
		cwd:     cwd,
		theme:   theme,
	}
}

func (f providerForm) Update(msg tea.Msg) (providerForm, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEsc:
			f.canceled = true
			f.done = true
			return f, nil
		case tea.KeyEnter:
			if f.focused == fieldModel {
				f.done = true
				return f, nil
			}
			return f.nextField(), nil
		case tea.KeyTab, tea.KeyDown:
			return f.nextField(), nil
		case tea.KeyShiftTab, tea.KeyUp:
			return f.prevField(), nil
		}
	}
	var cmd tea.Cmd
	f.fields[f.focused], cmd = f.fields[f.focused].Update(msg)
	return f, cmd
}

func (f providerForm) nextField() providerForm {
	f.fields[f.focused].Blur()
	f.focused = (f.focused + 1) % fieldCount
	f.fields[f.focused].Focus()
	return f
}

func (f providerForm) prevField() providerForm {
	f.fields[f.focused].Blur()
	f.focused = (f.focused - 1 + fieldCount) % fieldCount
	f.fields[f.focused].Focus()
	return f
}

func (f providerForm) View() string {
	t := f.theme
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#5f87d7")).
		Padding(1, 2).
		Width(62)

	title := t.TableHeader.Render("Provider Config")
	content := title + "\n\n"
	labels := []string{"URL", "Key", "Model"}
	for i := 0; i < fieldCount; i++ {
		indicator := "  "
		if i == f.focused {
			indicator = t.IndicatorAgent.Render("* ")
		}
		content += indicator + t.StatusKey.Render(labels[i]+": ") + f.fields[i].View() + "\n"
	}
	content += "\n" + t.Muted.Render("  Tab: next  Enter: save  Esc: cancel")

	return box.Render(content)
}

// Apply writes the form values back to config and rebuilds the provider.
func (f providerForm) Apply(cfg *config.Config, providers *llm.Registry) string {
	if f.canceled {
		return "Provider config canceled."
	}
	url := f.fields[fieldURL].Value()
	key := f.fields[fieldKey].Value()
	model := f.fields[fieldModel].Value()

	providerName := cfg.Providers.Default.Name
	if providerName == "" || providerName == "lmstudio" {
		cfg.Providers.LMStudio.BaseURL = url
		cfg.Providers.LMStudio.APIKey = key
		providers.Register(llm.NewOpenAICompatible("lmstudio", cfg.Providers.LMStudio))
	} else {
		cfg.Providers.OpenAICompatible.BaseURL = url
		cfg.Providers.OpenAICompatible.APIKey = key
		providers.Register(llm.NewOpenAICompatible("openai_compatible", cfg.Providers.OpenAICompatible))
	}
	if model != "" {
		cfg.Models["chat"] = model
	}

	// Persist to .forge/config.toml
	f.persistConfig(*cfg)

	return f.theme.Success.Render("Provider updated: " + url + " model=" + model)
}

func (f providerForm) persistConfig(cfg config.Config) {
	dir := filepath.Join(f.cwd, ".forge")
	_ = os.MkdirAll(dir, 0o755)
	data, err := toml.Marshal(cfg)
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, "config.toml"), data, 0o644)
}
