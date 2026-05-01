package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// MCPResourceProvider is the slice of mcp.Manager that the resource tools need.
// Defined as an interface so the tools package does not import internal/mcp
// (which already imports tools, so a direct dependency would cycle).
type MCPResourceProvider interface {
	ListResources(ctx context.Context) ([]MCPResourceInfo, error)
	ReadResource(ctx context.Context, server, uri string) ([]MCPResourceContent, error)
}

// MCPResourceInfo mirrors mcp.ResourceInfo but lives in the tools package so
// the interface above does not pull in internal/mcp.
type MCPResourceInfo struct {
	URI         string
	Name        string
	Description string
	MIMEType    string
	Server      string
}

// MCPResourceContent mirrors mcp.ResourceContent.
type MCPResourceContent struct {
	URI      string
	MIMEType string
	Text     string
	Blob     string
}

type listMcpResourcesTool struct {
	provider MCPResourceProvider
}

func (listMcpResourcesTool) Name() string { return "list_mcp_resources" }
func (listMcpResourcesTool) Description() string {
	return "List resources exposed by every connected MCP server (resources/list)."
}
func (listMcpResourcesTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"server":{"type":"string","description":"optional: filter to one server name"}}}`)
}
func (listMcpResourcesTool) Permission(Context, json.RawMessage) PermissionRequest {
	return PermissionRequest{Decision: PermissionAllow}
}
func (t listMcpResourcesTool) Run(ctx Context, input json.RawMessage) (Result, error) {
	var req struct {
		Server string `json:"server"`
	}
	if len(input) > 0 {
		_ = json.Unmarshal(input, &req)
	}
	resources, err := t.provider.ListResources(context.Background())
	if err != nil {
		return Result{}, err
	}
	if req.Server != "" {
		filtered := resources[:0]
		for _, r := range resources {
			if r.Server == req.Server {
				filtered = append(filtered, r)
			}
		}
		resources = filtered
	}
	var b strings.Builder
	for _, r := range resources {
		fmt.Fprintf(&b, "[%s] %s", r.Server, r.URI)
		if r.Name != "" && r.Name != r.URI {
			fmt.Fprintf(&b, " (%s)", r.Name)
		}
		if r.MIMEType != "" {
			fmt.Fprintf(&b, "  mime=%s", r.MIMEType)
		}
		if r.Description != "" {
			fmt.Fprintf(&b, "\n    %s", r.Description)
		}
		b.WriteByte('\n')
	}
	return Result{
		Title:   "MCP resources",
		Summary: fmt.Sprintf("%d resources", len(resources)),
		Content: []ContentBlock{{Type: "text", Text: strings.TrimRight(b.String(), "\n")}},
	}, nil
}

type readMcpResourceTool struct {
	provider MCPResourceProvider
}

func (readMcpResourceTool) Name() string { return "read_mcp_resource" }
func (readMcpResourceTool) Description() string {
	return "Read an MCP resource by URI (resources/read). Server is optional; when omitted the runtime tries every connected server."
}
func (readMcpResourceTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","required":["uri"],"properties":{"uri":{"type":"string"},"server":{"type":"string","description":"optional: server name to read from"}}}`)
}
func (readMcpResourceTool) Permission(_ Context, _ json.RawMessage) PermissionRequest {
	return PermissionRequest{
		Decision: PermissionAsk,
		Reason:   "Reading an MCP resource pulls remote content into the conversation.",
	}
}
func (t readMcpResourceTool) Run(ctx Context, input json.RawMessage) (Result, error) {
	var req struct {
		URI    string `json:"uri"`
		Server string `json:"server"`
	}
	if err := json.Unmarshal(input, &req); err != nil {
		return Result{}, err
	}
	if req.URI == "" {
		return Result{}, fmt.Errorf("uri is required")
	}
	contents, err := t.provider.ReadResource(context.Background(), req.Server, req.URI)
	if err != nil {
		return Result{}, err
	}
	blocks := make([]ContentBlock, 0, len(contents))
	var snippet string
	for _, c := range contents {
		if c.Text != "" {
			blocks = append(blocks, ContentBlock{Type: "text", Text: c.Text})
			if snippet == "" {
				snippet = c.Text
			}
			continue
		}
		if c.Blob != "" {
			blocks = append(blocks, ContentBlock{
				Type: "text",
				Text: fmt.Sprintf("[binary content omitted: %d base64 bytes, mime=%s]", len(c.Blob), c.MIMEType),
			})
		}
	}
	if len(snippet) > 120 {
		snippet = snippet[:120] + "..."
	}
	if snippet == "" {
		snippet = fmt.Sprintf("%d content blocks", len(contents))
	}
	return Result{
		Title:   "MCP resource " + req.URI,
		Summary: snippet,
		Content: blocks,
	}, nil
}

// RegisterMCPResourceTools replaces the noop stubs for list_mcp_resources and
// read_mcp_resource with real implementations backed by the given provider.
// Idempotent and safe to call after RegisterBuiltins.
func RegisterMCPResourceTools(registry *Registry, provider MCPResourceProvider) {
	if provider == nil {
		return
	}
	registry.Register(listMcpResourcesTool{provider: provider}, "ListMcpResourcesTool")
	registry.Register(readMcpResourceTool{provider: provider}, "ReadMcpResourceTool")
}
