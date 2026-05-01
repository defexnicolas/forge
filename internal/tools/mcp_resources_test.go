package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type fakeMCPProvider struct {
	resources []MCPResourceInfo
	contents  map[string][]MCPResourceContent
}

func (f *fakeMCPProvider) ListResources(ctx context.Context) ([]MCPResourceInfo, error) {
	return f.resources, nil
}

func (f *fakeMCPProvider) ReadResource(ctx context.Context, server, uri string) ([]MCPResourceContent, error) {
	if c, ok := f.contents[uri]; ok {
		return c, nil
	}
	return nil, nil
}

func TestRegisterMCPResourceToolsReplacesNoopStubs(t *testing.T) {
	registry := NewRegistry()
	RegisterBuiltins(registry)

	// Sanity: the stub is registered as a noopTool out of the box.
	stub, ok := registry.Get("list_mcp_resources")
	if !ok {
		t.Fatal("list_mcp_resources not registered as a stub")
	}
	if _, isNoop := stub.(noopTool); !isNoop {
		t.Fatalf("expected stub to be noopTool, got %T", stub)
	}

	provider := &fakeMCPProvider{
		resources: []MCPResourceInfo{
			{URI: "file://a.txt", Name: "a", Server: "demo", MIMEType: "text/plain"},
			{URI: "file://b.txt", Name: "b", Server: "demo"},
		},
	}
	RegisterMCPResourceTools(registry, provider)

	real, ok := registry.Get("list_mcp_resources")
	if !ok {
		t.Fatal("list_mcp_resources missing after RegisterMCPResourceTools")
	}
	if _, stillNoop := real.(noopTool); stillNoop {
		t.Fatalf("expected real tool to replace noopTool, still got %T", real)
	}

	result, err := real.Run(Context{}, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(result.Summary, "2 resources") {
		t.Errorf("unexpected summary: %q", result.Summary)
	}
	if len(result.Content) == 0 || !strings.Contains(result.Content[0].Text, "file://a.txt") {
		t.Errorf("listing did not include the resource URIs: %#v", result.Content)
	}

	// alias should also be re-bound to the real tool
	aliased, ok := registry.Get("ListMcpResourcesTool")
	if !ok || aliased.Name() != "list_mcp_resources" {
		t.Errorf("alias not rebound: ok=%v name=%q", ok, aliased.Name())
	}
}

func TestReadMcpResourceRequiresURI(t *testing.T) {
	registry := NewRegistry()
	RegisterBuiltins(registry)
	RegisterMCPResourceTools(registry, &fakeMCPProvider{})

	tool, _ := registry.Get("read_mcp_resource")
	if _, err := tool.Run(Context{}, json.RawMessage(`{}`)); err == nil {
		t.Fatal("expected error for missing uri")
	}
}

func TestReadMcpResourceReadsContent(t *testing.T) {
	provider := &fakeMCPProvider{
		contents: map[string][]MCPResourceContent{
			"file://x.txt": {{URI: "file://x.txt", Text: "hello world"}},
		},
	}
	registry := NewRegistry()
	RegisterBuiltins(registry)
	RegisterMCPResourceTools(registry, provider)
	tool, _ := registry.Get("read_mcp_resource")
	result, err := tool.Run(Context{}, json.RawMessage(`{"uri":"file://x.txt"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(result.Content) != 1 || result.Content[0].Text != "hello world" {
		t.Errorf("unexpected content: %#v", result.Content)
	}
}

func TestRegisterMCPResourceToolsNilProviderIsNoop(t *testing.T) {
	registry := NewRegistry()
	RegisterBuiltins(registry)
	before, _ := registry.Get("list_mcp_resources")
	RegisterMCPResourceTools(registry, nil)
	after, _ := registry.Get("list_mcp_resources")
	// nil provider should leave the stub unchanged
	if _, isNoop := after.(noopTool); !isNoop {
		t.Errorf("nil provider must leave stub unchanged, got %T (was %T)", after, before)
	}
}

func TestListMcpResourcesFilterByServer(t *testing.T) {
	provider := &fakeMCPProvider{
		resources: []MCPResourceInfo{
			{URI: "a", Server: "alpha"},
			{URI: "b", Server: "beta"},
			{URI: "c", Server: "alpha"},
		},
	}
	registry := NewRegistry()
	RegisterBuiltins(registry)
	RegisterMCPResourceTools(registry, provider)
	tool, _ := registry.Get("list_mcp_resources")
	result, err := tool.Run(Context{}, json.RawMessage(`{"server":"alpha"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(result.Summary, "2 resources") {
		t.Errorf("server filter did not narrow results: %q", result.Summary)
	}
}
