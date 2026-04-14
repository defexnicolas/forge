package tui

import (
	"strings"
	"testing"
)

func TestHelpAndAutocompleteUseCommandDescriptors(t *testing.T) {
	m := model{theme: DefaultTheme()}
	help := stripAnsi(m.helpText())
	suggestions := Suggest("/", t.TempDir())
	suggested := map[string]bool{}
	for _, item := range suggestions {
		suggested[item] = true
	}

	for _, cmd := range tuiCommands {
		if !strings.Contains(help, cmd.Usage) {
			t.Fatalf("help missing command usage %q in:\n%s", cmd.Usage, help)
		}
		if !suggested[cmd.Name] {
			t.Fatalf("autocomplete missing command %q in %v", cmd.Name, suggestions)
		}
	}
}
