package tui

import "github.com/charmbracelet/lipgloss"

// Theme defines the visual style for the TUI.
type Theme struct {
	Name string

	StatusBar    lipgloss.Style
	StatusKey    lipgloss.Style
	StatusValue  lipgloss.Style
	StatusActive lipgloss.Style

	UserPrefix    lipgloss.Style
	AgentPrefix   lipgloss.Style
	AgentText     lipgloss.Style
	ToolCallStyle lipgloss.Style
	ToolResult    lipgloss.Style
	ErrorStyle    lipgloss.Style
	ApprovalStyle lipgloss.Style
	DoneStyle     lipgloss.Style
	Separator     lipgloss.Style
	InputBorder   lipgloss.Style

	Footer    lipgloss.Style
	FooterKey lipgloss.Style

	IndicatorAgent lipgloss.Style
	IndicatorTool  lipgloss.Style
	IndicatorError lipgloss.Style
	IndicatorDone  lipgloss.Style
	IndicatorUser  lipgloss.Style

	TableHeader lipgloss.Style
	TableRow    lipgloss.Style
	Muted       lipgloss.Style
	Accent      lipgloss.Style
	Success     lipgloss.Style
	Warning     lipgloss.Style

	// ThinkingBorder wraps the <think>...</think> block. Tier 1.3 replaces
	// the ASCII frame with a proper lipgloss border using this style.
	ThinkingBorder lipgloss.Style
}

// ThemeNames returns available theme preset names, with JSON-loaded themes
// appended after the built-ins.
func ThemeNames() []string {
	names := []string{"default", "light", "ocean", "mono"}
	names = append(names, customThemeNames()...)
	return names
}

// GetTheme returns a theme preset by name. Built-ins win over JSON themes
// with the same name so a misspelled custom override can't silently hide
// the default.
func GetTheme(name string) Theme {
	switch name {
	case "light":
		return LightTheme()
	case "ocean":
		return OceanTheme()
	case "mono":
		return MonoTheme()
	case "default", "":
		return DefaultTheme()
	}
	if t, ok := getCustomTheme(name); ok {
		return t
	}
	return DefaultTheme()
}

func DefaultTheme() Theme {
	cyan := lipgloss.Color("86")
	green := lipgloss.Color("78")
	yellow := lipgloss.Color("220")
	red := lipgloss.Color("203")
	purple := lipgloss.Color("141")
	blue := lipgloss.Color("75")
	dim := lipgloss.Color("245")
	bright := lipgloss.Color("255")

	return buildTheme("default", cyan, green, yellow, red, purple, blue, dim, bright,
		lipgloss.Color("236"), lipgloss.Color("234"))
}

func LightTheme() Theme {
	cyan := lipgloss.Color("30")
	green := lipgloss.Color("28")
	yellow := lipgloss.Color("136")
	red := lipgloss.Color("124")
	purple := lipgloss.Color("128")
	blue := lipgloss.Color("25")
	dim := lipgloss.Color("245")
	bright := lipgloss.Color("232")

	return buildTheme("light", cyan, green, yellow, red, purple, blue, dim, bright,
		lipgloss.Color("254"), lipgloss.Color("231"))
}

func OceanTheme() Theme {
	cyan := lipgloss.Color("44")
	green := lipgloss.Color("48")
	yellow := lipgloss.Color("186")
	red := lipgloss.Color("167")
	purple := lipgloss.Color("105")
	blue := lipgloss.Color("69")
	dim := lipgloss.Color("243")
	bright := lipgloss.Color("195")

	return buildTheme("ocean", cyan, green, yellow, red, purple, blue, dim, bright,
		lipgloss.Color("236"), lipgloss.Color("17"))
}

func MonoTheme() Theme {
	white := lipgloss.Color("255")
	lgray := lipgloss.Color("250")
	dgray := lipgloss.Color("240")

	return buildTheme("mono", white, lgray, lgray, white, lgray, lgray, dgray, white,
		lipgloss.Color("236"), lipgloss.Color("233"))
}

func buildTheme(name string, cyan, green, yellow, red, purple, blue, dim, bright, barBg, inputBg lipgloss.Color) Theme {
	return Theme{
		Name: name,
		StatusBar: lipgloss.NewStyle().
			Background(barBg).Foreground(bright).Bold(true).Padding(0, 1),
		StatusKey:    lipgloss.NewStyle().Foreground(dim),
		StatusValue:  lipgloss.NewStyle().Foreground(cyan).Bold(true),
		StatusActive: lipgloss.NewStyle().Foreground(green).Bold(true),

		UserPrefix:    lipgloss.NewStyle().Foreground(blue).Bold(true),
		AgentPrefix:   lipgloss.NewStyle().Foreground(purple).Bold(true),
		AgentText:     lipgloss.NewStyle().Foreground(bright).PaddingLeft(3),
		ToolCallStyle: lipgloss.NewStyle().Foreground(yellow),
		ToolResult:    lipgloss.NewStyle().Foreground(dim),
		ErrorStyle:    lipgloss.NewStyle().Foreground(red).Bold(true),
		ApprovalStyle: lipgloss.NewStyle().Foreground(yellow).Bold(true),
		DoneStyle:     lipgloss.NewStyle().Foreground(dim).Italic(true),
		Separator:     lipgloss.NewStyle().Foreground(dim),
		InputBorder: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).BorderForeground(dim).Padding(0, 1),

		Footer:    lipgloss.NewStyle().Foreground(dim),
		FooterKey: lipgloss.NewStyle().Foreground(bright).Bold(true),

		IndicatorAgent: lipgloss.NewStyle().Foreground(purple).Bold(true),
		IndicatorTool:  lipgloss.NewStyle().Foreground(yellow).Bold(true),
		IndicatorError: lipgloss.NewStyle().Foreground(red).Bold(true),
		IndicatorDone:  lipgloss.NewStyle().Foreground(cyan),
		IndicatorUser:  lipgloss.NewStyle().Foreground(blue).Bold(true),

		TableHeader: lipgloss.NewStyle().Foreground(cyan).Bold(true).Underline(true),
		TableRow:    lipgloss.NewStyle().Foreground(bright),
		Muted:       lipgloss.NewStyle().Foreground(dim),
		Accent:      lipgloss.NewStyle().Foreground(cyan),
		Success:     lipgloss.NewStyle().Foreground(green),
		Warning:     lipgloss.NewStyle().Foreground(yellow),

		ThinkingBorder: lipgloss.NewStyle().
			Border(lipgloss.ThickBorder()).BorderForeground(dim).
			Foreground(dim).Padding(0, 1),
	}
}

func (t Theme) SeparatorLine(width int) string {
	if width <= 0 {
		width = 80
	}
	line := ""
	for i := 0; i < width; i++ {
		line += "-"
	}
	return t.Separator.Render(line)
}

func (t Theme) FormatTable(headers []string, rows [][]string) string {
	if len(headers) == 0 {
		return ""
	}
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = len(h)
	}
	for _, row := range rows {
		for i := 0; i < len(row) && i < len(widths); i++ {
			if len(row[i]) > widths[i] {
				widths[i] = len(row[i])
			}
		}
	}
	var b []string
	var hdr string
	for i, h := range headers {
		hdr += t.TableHeader.Render(pad(h, widths[i])) + "  "
	}
	b = append(b, hdr)
	for _, row := range rows {
		var line string
		for i := 0; i < len(headers); i++ {
			val := ""
			if i < len(row) {
				val = row[i]
			}
			line += t.TableRow.Render(pad(val, widths[i])) + "  "
		}
		b = append(b, line)
	}
	return joinLines(b)
}

func pad(s string, width int) string {
	for len(s) < width {
		s += " "
	}
	return s
}

func joinLines(lines []string) string {
	result := ""
	for i, l := range lines {
		if i > 0 {
			result += "\n"
		}
		result += l
	}
	return result
}
