package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"forge/internal/config"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/pelletier/go-toml/v2"
)

const (
	yarnFieldProfile = iota
	yarnFieldModelContext
	yarnFieldBudget
	yarnFieldReserve
	yarnFieldMaxNodes
	yarnFieldMaxFileBytes
	yarnFieldHistoryEvents
	yarnFieldPins
	yarnFieldMentions
	yarnFieldCompactEvents
	yarnFieldCompactTranscript
	yarnFieldCount
)

type yarnSettingsForm struct {
	fields   [yarnFieldCount]textinput.Model
	focused  int
	done     bool
	canceled bool
	cwd      string
	theme    Theme
	errMsg   string
}

func newYarnSettingsForm(cwd string, cfg config.Config, theme Theme) yarnSettingsForm {
	labels := []string{
		"Profile",
		"Model ctx",
		"YARN budget",
		"Reserve out",
		"Max nodes",
		"Max file bytes",
		"History events",
		"Pins",
		"Mentions",
		"Compact events",
		"Compact chars",
	}
	values := []string{
		cfg.Context.Yarn.Profile,
		strconv.Itoa(cfg.Context.ModelContextTokens),
		strconv.Itoa(cfg.Context.BudgetTokens),
		strconv.Itoa(cfg.Context.ReserveOutputTokens),
		strconv.Itoa(cfg.Context.Yarn.MaxNodes),
		strconv.Itoa(cfg.Context.Yarn.MaxFileBytes),
		strconv.Itoa(cfg.Context.Yarn.HistoryEvents),
		cfg.Context.Yarn.Pins,
		cfg.Context.Yarn.Mentions,
		strconv.Itoa(cfg.Context.Yarn.CompactEvents),
		strconv.Itoa(cfg.Context.Yarn.CompactTranscriptChars),
	}
	form := yarnSettingsForm{cwd: cwd, theme: theme}
	for i := 0; i < yarnFieldCount; i++ {
		field := textinput.New()
		field.Prompt = fmt.Sprintf("  %-15s ", labels[i])
		field.SetValue(values[i])
		field.Width = 24
		if i == 0 {
			field.Focus()
		}
		form.fields[i] = field
	}
	return form
}

func (f yarnSettingsForm) Update(msg tea.Msg) (yarnSettingsForm, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEsc:
			f.canceled = true
			f.done = true
			return f, nil
		case tea.KeyEnter:
			if f.focused == yarnFieldCount-1 {
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

func (f yarnSettingsForm) nextField() yarnSettingsForm {
	f.fields[f.focused].Blur()
	f.focused = (f.focused + 1) % yarnFieldCount
	f.fields[f.focused].Focus()
	return f
}

func (f yarnSettingsForm) prevField() yarnSettingsForm {
	f.fields[f.focused].Blur()
	f.focused = (f.focused - 1 + yarnFieldCount) % yarnFieldCount
	f.fields[f.focused].Focus()
	return f
}

func (f yarnSettingsForm) View() string {
	t := f.theme
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#87af87")).
		Padding(1, 2).
		Width(74)

	content := t.TableHeader.Render("YARN Settings") + "\n\n"
	for i := 0; i < yarnFieldCount; i++ {
		indicator := "  "
		if i == f.focused {
			indicator = t.IndicatorAgent.Render("> ")
		}
		content += indicator + f.fields[i].View() + "\n"
	}
	content += "\n" + t.Muted.Render("Pins: always|scored|off  Mentions: always|scored")
	content += "\n" + t.Muted.Render("Tab: next  Enter: save  Esc: cancel")
	return box.Render(content)
}

func (f yarnSettingsForm) Apply(cfg *config.Config) string {
	if f.canceled {
		return "YARN settings canceled."
	}
	cfg.Context.Yarn.Profile = strings.ToUpper(strings.TrimSpace(f.fields[yarnFieldProfile].Value()))
	cfg.Context.ModelContextTokens = parsePositive(f.fields[yarnFieldModelContext].Value(), cfg.Context.ModelContextTokens)
	cfg.Context.BudgetTokens = parsePositive(f.fields[yarnFieldBudget].Value(), cfg.Context.BudgetTokens)
	cfg.Context.ReserveOutputTokens = parsePositive(f.fields[yarnFieldReserve].Value(), cfg.Context.ReserveOutputTokens)
	cfg.Context.Yarn.MaxNodes = parsePositive(f.fields[yarnFieldMaxNodes].Value(), cfg.Context.Yarn.MaxNodes)
	cfg.Context.Yarn.MaxFileBytes = parsePositive(f.fields[yarnFieldMaxFileBytes].Value(), cfg.Context.Yarn.MaxFileBytes)
	cfg.Context.Yarn.HistoryEvents = parsePositive(f.fields[yarnFieldHistoryEvents].Value(), cfg.Context.Yarn.HistoryEvents)
	cfg.Context.Yarn.Pins = normalizeYarnMode(f.fields[yarnFieldPins].Value(), "always", true)
	cfg.Context.Yarn.Mentions = normalizeYarnMode(f.fields[yarnFieldMentions].Value(), "always", false)
	cfg.Context.Yarn.CompactEvents = parsePositive(f.fields[yarnFieldCompactEvents].Value(), cfg.Context.Yarn.CompactEvents)
	cfg.Context.Yarn.CompactTranscriptChars = parsePositive(f.fields[yarnFieldCompactTranscript].Value(), cfg.Context.Yarn.CompactTranscriptChars)
	config.Normalize(cfg)
	f.persistConfig(*cfg)
	return f.theme.Success.Render("YARN settings saved.") + yarnOverflowWarning(*cfg, f.theme)
}

func (f yarnSettingsForm) persistConfig(cfg config.Config) {
	dir := filepath.Join(f.cwd, ".forge")
	_ = os.MkdirAll(dir, 0o755)
	data, err := toml.Marshal(cfg)
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, "config.toml"), data, 0o644)
}

func parsePositive(value string, fallback int) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func normalizeYarnMode(value, fallback string, allowOff bool) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "always", "scored":
		return value
	case "off":
		if allowOff {
			return value
		}
	}
	return fallback
}

// promptOverheadTokens estimates fixed per-request overhead: system prompt + tool defs + delimiters.
const promptOverheadTokens = 2500

func yarnOverflowWarning(cfg config.Config, theme Theme) string {
	if cfg.Context.ModelContextTokens <= 0 {
		return ""
	}
	used := cfg.Context.BudgetTokens + cfg.Context.ReserveOutputTokens + promptOverheadTokens
	if used <= cfg.Context.ModelContextTokens {
		return ""
	}
	return "\n" + theme.Warning.Render(fmt.Sprintf("Warning: YARN budget + reserved output + ~%d overhead (%d total) exceeds model context (%d).", promptOverheadTokens, used, cfg.Context.ModelContextTokens))
}
