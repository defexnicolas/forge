package app

import (
	"context"

	"forge/internal/mcp"
	"forge/internal/tools"
)

// mcpResourceAdapter bridges *mcp.Manager (which speaks mcp.ResourceInfo /
// mcp.ResourceContent) to tools.MCPResourceProvider (which speaks the
// equivalent types declared in the tools package). The two types exist
// separately so internal/tools does not import internal/mcp -- mcp already
// imports tools, and a back-edge would cycle.
type mcpResourceAdapter struct {
	m *mcp.Manager
}

func (a mcpResourceAdapter) ListResources(ctx context.Context) ([]tools.MCPResourceInfo, error) {
	resources, err := a.m.ListResources(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]tools.MCPResourceInfo, len(resources))
	for i, r := range resources {
		out[i] = tools.MCPResourceInfo{
			URI:         r.URI,
			Name:        r.Name,
			Description: r.Description,
			MIMEType:    r.MIMEType,
			Server:      r.Server,
		}
	}
	return out, nil
}

func (a mcpResourceAdapter) ReadResource(ctx context.Context, server, uri string) ([]tools.MCPResourceContent, error) {
	contents, err := a.m.ReadResource(ctx, server, uri)
	if err != nil {
		return nil, err
	}
	out := make([]tools.MCPResourceContent, len(contents))
	for i, c := range contents {
		out[i] = tools.MCPResourceContent{
			URI:      c.URI,
			MIMEType: c.MIMEType,
			Text:     c.Text,
			Blob:     c.Blob,
		}
	}
	return out, nil
}
