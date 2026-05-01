package tui

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// describeMCPResources renders /mcp resources -- a flat listing of every
// resource exposed by every connected server, grouped by server.
func (m *model) describeMCPResources() string {
	if m.options.MCP == nil {
		return "MCP not loaded."
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resources, err := m.options.MCP.ListResources(ctx)
	if err != nil {
		return m.theme.ErrorStyle.Render("MCP resources: " + err.Error())
	}
	if len(resources) == 0 {
		return "MCP: no resources exposed by any connected server."
	}
	var b strings.Builder
	current := ""
	for _, r := range resources {
		if r.Server != current {
			if current != "" {
				b.WriteString("\n")
			}
			fmt.Fprintf(&b, "[%s]\n", r.Server)
			current = r.Server
		}
		fmt.Fprintf(&b, "  %s", r.URI)
		if r.Name != "" && r.Name != r.URI {
			fmt.Fprintf(&b, "  (%s)", r.Name)
		}
		if r.MIMEType != "" {
			fmt.Fprintf(&b, "  mime=%s", r.MIMEType)
		}
		b.WriteString("\n")
		if r.Description != "" {
			fmt.Fprintf(&b, "    %s\n", r.Description)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// describeMCPPrompts renders /mcp prompts -- a flat listing of every prompt
// template exposed by every connected server, grouped by server, with
// argument signatures inlined so the user knows how to /mcp prompt-get them
// later.
func (m *model) describeMCPPrompts() string {
	if m.options.MCP == nil {
		return "MCP not loaded."
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	prompts, err := m.options.MCP.ListPrompts(ctx)
	if err != nil {
		return m.theme.ErrorStyle.Render("MCP prompts: " + err.Error())
	}
	if len(prompts) == 0 {
		return "MCP: no prompts exposed by any connected server."
	}
	var b strings.Builder
	current := ""
	for _, p := range prompts {
		if p.Server != current {
			if current != "" {
				b.WriteString("\n")
			}
			fmt.Fprintf(&b, "[%s]\n", p.Server)
			current = p.Server
		}
		fmt.Fprintf(&b, "  %s", p.Name)
		if len(p.Arguments) > 0 {
			parts := make([]string, 0, len(p.Arguments))
			for _, a := range p.Arguments {
				if a.Required {
					parts = append(parts, a.Name+"!")
				} else {
					parts = append(parts, a.Name)
				}
			}
			fmt.Fprintf(&b, "(%s)", strings.Join(parts, ", "))
		}
		b.WriteString("\n")
		if p.Description != "" {
			fmt.Fprintf(&b, "    %s\n", p.Description)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
