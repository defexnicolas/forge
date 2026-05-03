package tui

import (
	"strings"

	"forge/internal/config"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// webSearchForm is a two-phase selector:
//
//	phase 1 — pick a provider from a fixed list (arrow keys + Enter)
//	phase 2 — enter API key only if the provider requires one
//
// No free-text provider names, no env-var field, no base URL override.
// The defaults for each provider are baked into knownWebSearchProviders
// so the user never has to remember endpoint shapes. Power users who
// want env-var-backed keys or custom base URLs still have direct toml
// access — this form is the friendly path, not the only path.
type webSearchForm struct {
	providers []webSearchProvider
	cursor    int

	phase    webSearchPhase
	apiInput textinput.Model

	canceled bool
	done     bool
	theme    Theme
}

type webSearchPhase int

const (
	wsPhaseSelect webSearchPhase = iota
	wsPhaseAPIKey
)

// webSearchProvider describes one selectable backend. RequiresAPIKey drives
// whether the form jumps to the key-entry phase after Enter. DefaultBase
// is the canonical endpoint forge will use when this provider is picked
// (the user does not see or edit it).
type webSearchProvider struct {
	ID            string
	Label         string
	RequiresAPIKey bool
	DefaultBase   string
}

func knownWebSearchProviders() []webSearchProvider {
	return []webSearchProvider{
		{
			ID:    "duckduckgo",
			Label: "DuckDuckGo  (no API key, free)",
		},
		{
			ID:             "ollama",
			Label:          "Ollama Cloud  (API key required)",
			RequiresAPIKey: true,
			DefaultBase:    "https://ollama.com",
		},
	}
}

func newWebSearchForm(cfg config.Config, theme Theme) webSearchForm {
	providers := knownWebSearchProviders()
	cursor := 0
	for i, p := range providers {
		if strings.EqualFold(strings.TrimSpace(cfg.WebSearch.Provider), p.ID) {
			cursor = i
			break
		}
	}
	return webSearchForm{
		providers: providers,
		cursor:    cursor,
		phase:     wsPhaseSelect,
		theme:     theme,
	}
}

func (f webSearchForm) Update(msg tea.Msg) (webSearchForm, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch f.phase {
		case wsPhaseSelect:
			switch msg.Type {
			case tea.KeyEsc:
				f.canceled = true
				f.done = true
				return f, nil
			case tea.KeyUp:
				if f.cursor > 0 {
					f.cursor--
				}
				return f, nil
			case tea.KeyDown:
				if f.cursor < len(f.providers)-1 {
					f.cursor++
				}
				return f, nil
			case tea.KeyEnter:
				selected := f.providers[f.cursor]
				if !selected.RequiresAPIKey {
					f.done = true
					return f, nil
				}
				// Move to API key phase. Pre-focus a fresh textinput so
				// the user can start typing immediately.
				input := textinput.New()
				input.Placeholder = "paste API key here"
				input.EchoMode = textinput.EchoPassword
				input.EchoCharacter = '•'
				input.Width = 50
				input.Prompt = "  > "
				input.Focus()
				f.apiInput = input
				f.phase = wsPhaseAPIKey
				return f, nil
			}
		case wsPhaseAPIKey:
			switch msg.Type {
			case tea.KeyEsc:
				// Esc from key phase backs up to the provider list rather
				// than canceling the whole form, so the user can pick a
				// different provider without restarting.
				f.phase = wsPhaseSelect
				return f, nil
			case tea.KeyEnter:
				f.done = true
				return f, nil
			}
			var cmd tea.Cmd
			f.apiInput, cmd = f.apiInput.Update(msg)
			return f, cmd
		}
	}
	if f.phase == wsPhaseAPIKey {
		var cmd tea.Cmd
		f.apiInput, cmd = f.apiInput.Update(msg)
		return f, cmd
	}
	return f, nil
}

func (f webSearchForm) View() string {
	t := f.theme
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#5f87d7")).
		Padding(1, 2).
		Width(60)

	var b strings.Builder
	b.WriteString(t.TableHeader.Render("Web Search Provider") + "\n\n")

	switch f.phase {
	case wsPhaseSelect:
		for i, p := range f.providers {
			marker := "  "
			if i == f.cursor {
				marker = t.IndicatorAgent.Render("> ")
			}
			b.WriteString(marker + p.Label + "\n")
		}
		b.WriteString("\n" + t.Muted.Render("Up/Down: select  Enter: choose  Esc: cancel"))
	case wsPhaseAPIKey:
		selected := f.providers[f.cursor]
		b.WriteString(t.StatusKey.Render("Provider: ") + selected.Label + "\n\n")
		b.WriteString(t.StatusKey.Render("API Key:") + "\n")
		b.WriteString(f.apiInput.View() + "\n\n")
		b.WriteString(t.Muted.Render("Enter: save  Esc: back to provider list"))
	}
	return box.Render(b.String())
}

// ApplyInMemory writes the chosen provider + API key into cfg.WebSearch.
// Clears APIKeyEnv and BaseURL — the simplified form does not expose them,
// so leaving stale values from a previous edit would surprise the user.
// Power users editing global.toml directly can still use those fields.
func (f webSearchForm) ApplyInMemory(cfg *config.Config) string {
	if f.canceled {
		return "Web Search settings canceled."
	}
	if f.cursor >= len(f.providers) {
		return f.theme.ErrorStyle.Render("Selection out of range.")
	}
	selected := f.providers[f.cursor]
	cfg.WebSearch.Provider = selected.ID
	cfg.WebSearch.BaseURL = selected.DefaultBase
	cfg.WebSearch.APIKeyEnv = ""
	if selected.RequiresAPIKey {
		key := strings.TrimSpace(f.apiInput.Value())
		if key != "" {
			cfg.WebSearch.APIKey = key
		}
		// If the user left it blank, keep whatever was previously stored
		// (lets them re-pick the same provider without retyping the key).
	} else {
		cfg.WebSearch.APIKey = ""
	}
	return f.theme.Success.Render("Web Search: " + selected.Label)
}
