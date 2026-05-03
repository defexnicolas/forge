package agent

import (
	"testing"
)

// TestMergePluginAgentsAddsToRegistry verifies that a plugin-supplied agent
// becomes a target the runtime can resolve via Subagents.Get. This is the
// key wiring that lets a Claude Code plugin's agents/<name>.md file
// participate in spawn_subagent the same way built-ins do.
func TestMergePluginAgentsAddsToRegistry(t *testing.T) {
	registry := DefaultSubagents()
	if _, ok := registry.Get("explorer-fast"); ok {
		t.Fatal("explorer-fast should not exist before merge")
	}
	MergePluginAgents(&registry, []PluginAgent{
		{
			Name:        "explorer-fast",
			Description: "Quick read-only explorer",
			Source:      "sample-plugin",
			Body:        "Be concise.",
			Tools:       []string{"read_file", "list_files"},
			ModelRole:   "explorer",
		},
	})
	got, ok := registry.Get("explorer-fast")
	if !ok {
		t.Fatal("explorer-fast missing after merge")
	}
	if got.ModelRole != "explorer" {
		t.Errorf("ModelRole = %q, want explorer", got.ModelRole)
	}
	if len(got.AllowedTools) != 2 {
		t.Errorf("AllowedTools = %v, want [read_file list_files]", got.AllowedTools)
	}
	if got.SystemBody != "Be concise." {
		t.Errorf("SystemBody lost: %q", got.SystemBody)
	}
}

// TestMergePluginAgentsAppliesSafeDefaults verifies that an agent
// frontmatter with no tools/model_role drops to read-only explorer
// defaults rather than inheriting nothing — a malformed plugin file must
// never silently gain mutating capabilities.
func TestMergePluginAgentsAppliesSafeDefaults(t *testing.T) {
	registry := DefaultSubagents()
	MergePluginAgents(&registry, []PluginAgent{{
		Name: "no-meta",
		Body: "do work",
	}})
	got, _ := registry.Get("no-meta")
	if got.ModelRole != "explorer" {
		t.Errorf("default ModelRole = %q, want explorer", got.ModelRole)
	}
	for _, want := range []string{"read_file", "list_files", "search_text", "search_files"} {
		found := false
		for _, tn := range got.AllowedTools {
			if tn == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("default tool %q missing from %v", want, got.AllowedTools)
		}
	}
	// No mutating tools must appear in defaults.
	for _, banned := range []string{"edit_file", "write_file", "apply_patch", "run_command"} {
		for _, tn := range got.AllowedTools {
			if tn == banned {
				t.Errorf("default AllowedTools contains mutating tool %q", banned)
			}
		}
	}
}
