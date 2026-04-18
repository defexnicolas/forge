package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/charmbracelet/lipgloss"
)

// themeFile describes the JSON payload accepted by LoadCustomThemes. Color
// fields accept either ANSI256 numbers ("86") or hex strings ("#6FD8E7").
// Missing fields fall back to the DefaultTheme value for that slot so users
// can override just the colors they care about.
type themeFile struct {
	Name    string `json:"name"`
	Cyan    string `json:"cyan,omitempty"`
	Green   string `json:"green,omitempty"`
	Yellow  string `json:"yellow,omitempty"`
	Red     string `json:"red,omitempty"`
	Purple  string `json:"purple,omitempty"`
	Blue    string `json:"blue,omitempty"`
	Dim     string `json:"dim,omitempty"`
	Bright  string `json:"bright,omitempty"`
	BarBg   string `json:"bar_bg,omitempty"`
	InputBg string `json:"input_bg,omitempty"`
}

var (
	customThemeMu sync.RWMutex
	customThemes  = map[string]Theme{}
)

// LoadCustomThemes scans dir for *.json files, parses each as a themeFile,
// and registers the resulting Theme under its Name (or the file stem when
// Name is omitted). Safe to call multiple times — later calls replace
// previously registered themes. Errors on individual files are logged and
// skipped so one bad file doesn't block the rest.
func LoadCustomThemes(dir string) error {
	if strings.TrimSpace(dir) == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	loaded := map[string]Theme{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		theme, name, err := loadThemeFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "theme %s: %v\n", path, err)
			continue
		}
		if name == "" {
			name = strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		}
		loaded[name] = theme
	}
	customThemeMu.Lock()
	customThemes = loaded
	customThemeMu.Unlock()
	return nil
}

func loadThemeFile(path string) (Theme, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Theme{}, "", err
	}
	var tf themeFile
	if err := json.Unmarshal(data, &tf); err != nil {
		return Theme{}, "", err
	}
	fallback := DefaultTheme()
	theme := buildTheme(tf.Name,
		parseColorOr(tf.Cyan, fallback, "cyan"),
		parseColorOr(tf.Green, fallback, "green"),
		parseColorOr(tf.Yellow, fallback, "yellow"),
		parseColorOr(tf.Red, fallback, "red"),
		parseColorOr(tf.Purple, fallback, "purple"),
		parseColorOr(tf.Blue, fallback, "blue"),
		parseColorOr(tf.Dim, fallback, "dim"),
		parseColorOr(tf.Bright, fallback, "bright"),
		parseColorOr(tf.BarBg, fallback, "barBg"),
		parseColorOr(tf.InputBg, fallback, "inputBg"),
	)
	return theme, tf.Name, nil
}

// parseColorOr converts a user-supplied color string into a lipgloss.Color.
// Accepts #rrggbb, #rgb, or plain ANSI256 numbers ("0".."255"). Falls back
// to a slot on the default theme when the string is empty or malformed so
// a partial JSON never produces a black-on-black theme.
func parseColorOr(raw string, fallback Theme, slot string) lipgloss.Color {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallbackColor(fallback, slot)
	}
	if strings.HasPrefix(raw, "#") {
		return lipgloss.Color(raw)
	}
	// ANSI256 index as-is (lipgloss accepts the numeric string).
	for _, r := range raw {
		if r < '0' || r > '9' {
			return lipgloss.Color(raw)
		}
	}
	return lipgloss.Color(raw)
}

func fallbackColor(theme Theme, slot string) lipgloss.Color {
	// lipgloss.Style exposes colors only via rendering; pull the raw color
	// from buildTheme by reading the default. These match DefaultTheme's
	// literal assignments.
	switch slot {
	case "cyan":
		return lipgloss.Color("86")
	case "green":
		return lipgloss.Color("78")
	case "yellow":
		return lipgloss.Color("220")
	case "red":
		return lipgloss.Color("203")
	case "purple":
		return lipgloss.Color("141")
	case "blue":
		return lipgloss.Color("75")
	case "dim":
		return lipgloss.Color("245")
	case "bright":
		return lipgloss.Color("255")
	case "barBg":
		return lipgloss.Color("236")
	case "inputBg":
		return lipgloss.Color("234")
	}
	return lipgloss.Color("255")
}

// getCustomTheme returns a Theme registered via LoadCustomThemes, plus true
// when a match exists. Case-insensitive name lookup matches the behavior
// of built-in GetTheme.
func getCustomTheme(name string) (Theme, bool) {
	customThemeMu.RLock()
	defer customThemeMu.RUnlock()
	if t, ok := customThemes[name]; ok {
		return t, true
	}
	lower := strings.ToLower(name)
	for k, v := range customThemes {
		if strings.ToLower(k) == lower {
			return v, true
		}
	}
	return Theme{}, false
}

// customThemeNames lists currently registered JSON theme names, sorted.
func customThemeNames() []string {
	customThemeMu.RLock()
	defer customThemeMu.RUnlock()
	names := make([]string, 0, len(customThemes))
	for k := range customThemes {
		names = append(names, k)
	}
	return names
}
