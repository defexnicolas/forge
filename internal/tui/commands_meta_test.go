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
	for _, sub := range []string{"panel", "full", "todos", "new", "refine"} {
		if !strings.Contains(help, sub) {
			t.Fatalf("help missing /plan subcommand %q in:\n%s", sub, help)
		}
	}
}

func TestProfileSubcommandAutocomplete(t *testing.T) {
	all := Suggest("/profile ", t.TempDir())
	for _, want := range []string{"/profile safe", "/profile normal", "/profile fast", "/profile trusted", "/profile yolo"} {
		if !containsSuggestion(all, want) {
			t.Fatalf("autocomplete missing %q in %v", want, all)
		}
	}

	filtered := Suggest("/profile tr", t.TempDir())
	if len(filtered) != 1 || filtered[0] != "/profile trusted" {
		t.Fatalf("filtered /profile autocomplete = %v, want [/profile trusted]", filtered)
	}
}

func TestPermissionsSubcommandAutocomplete(t *testing.T) {
	all := Suggest("/permissions ", t.TempDir())
	for _, want := range []string{"/permissions set", "/permissions trusted", "/permissions yolo"} {
		if !containsSuggestion(all, want) {
			t.Fatalf("autocomplete missing %q in %v", want, all)
		}
	}
}

func TestPlanSubcommandAutocomplete(t *testing.T) {
	all := Suggest("/plan ", t.TempDir())
	for _, want := range []string{"/plan panel", "/plan full", "/plan todos", "/plan new", "/plan refine"} {
		if !containsSuggestion(all, want) {
			t.Fatalf("autocomplete missing %q in %v", want, all)
		}
	}

	filtered := Suggest("/plan f", t.TempDir())
	if len(filtered) != 1 || filtered[0] != "/plan full" {
		t.Fatalf("filtered /plan autocomplete = %v, want [/plan full]", filtered)
	}
}

func containsSuggestion(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
