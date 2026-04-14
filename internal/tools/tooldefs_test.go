package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestToolDefsConvertsRegisteredTools(t *testing.T) {
	registry := NewRegistry()
	RegisterBuiltins(registry)

	defs := registry.ToolDefs(nil)
	if len(defs) == 0 {
		t.Fatal("expected tool definitions")
	}

	found := map[string]bool{}
	for _, def := range defs {
		if def.Type != "function" {
			t.Fatalf("expected type function, got %s", def.Type)
		}
		if def.Function.Name == "" {
			t.Fatal("expected non-empty tool name")
		}
		if def.Function.Description == "" {
			t.Fatalf("tool %s has empty description", def.Function.Name)
		}
		if !json.Valid(def.Function.Parameters) {
			t.Fatalf("tool %s has invalid parameters JSON", def.Function.Name)
		}
		found[def.Function.Name] = true
	}

	for _, expected := range []string{"read_file", "list_files", "search_text", "edit_file", "run_command"} {
		if !found[expected] {
			t.Fatalf("expected tool %s in ToolDefs output", expected)
		}
	}
}

func TestToolDefsFiltersByNames(t *testing.T) {
	registry := NewRegistry()
	RegisterBuiltins(registry)

	defs := registry.ToolDefs([]string{"read_file", "git_status"})
	if len(defs) != 2 {
		t.Fatalf("expected 2 tool defs, got %d", len(defs))
	}
	names := map[string]bool{}
	for _, d := range defs {
		names[d.Function.Name] = true
	}
	if !names["read_file"] || !names["git_status"] {
		t.Fatalf("unexpected tool defs: %v", names)
	}
}

func TestToolDefsResolvesAliases(t *testing.T) {
	registry := NewRegistry()
	RegisterBuiltins(registry)

	// "Read" is a Claude Code alias for "read_file"
	defs := registry.ToolDefs([]string{"Read"})
	if len(defs) != 1 {
		t.Fatalf("expected 1 tool def from alias, got %d", len(defs))
	}
	if defs[0].Function.Name != "read_file" {
		t.Fatalf("expected read_file, got %s", defs[0].Function.Name)
	}
}

func TestDescribeMarksStubTools(t *testing.T) {
	registry := NewRegistry()
	RegisterBuiltins(registry)

	status := map[string]string{}
	description := map[string]string{}
	for _, desc := range registry.Describe() {
		status[desc.Name] = desc.Status
		description[desc.Name] = desc.Description
	}

	for _, name := range []string{"list_mcp_resources", "read_mcp_resource", "lsp", "monitor"} {
		if status[name] != "stub" {
			t.Fatalf("expected %s to be marked stub, got %q", name, status[name])
		}
		if !strings.Contains(description[name], "implementation pending") {
			t.Fatalf("expected %s description to mention implementation pending, got %q", name, description[name])
		}
	}
	if status["read_file"] != "ready" {
		t.Fatalf("expected read_file to be ready, got %q", status["read_file"])
	}
}

func TestStubToolRunMessageIsExplicit(t *testing.T) {
	result, err := noopTool{name: "run_skill", description: "stub"}.Run(Context{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Summary, "Stub tool registered for compatibility") {
		t.Fatalf("unexpected stub summary: %q", result.Summary)
	}
}
