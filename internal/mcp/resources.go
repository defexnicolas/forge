package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ResourceInfo is a single resource exposed by an MCP server.
type ResourceInfo struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MIMEType    string `json:"mimeType,omitempty"`
	Server      string `json:"server"`
}

// ResourceContent is one returned block from resources/read.
type ResourceContent struct {
	URI      string `json:"uri"`
	MIMEType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     string `json:"blob,omitempty"` // base64-encoded binary content per spec
}

type resourcesListResult struct {
	Resources []ResourceInfo `json:"resources"`
}

type resourcesReadResult struct {
	Contents []ResourceContent `json:"contents"`
}

// connSendable lets us share the resources/* implementations between stdio and
// sse connections.
type connSendable interface {
	send(req jsonRPCRequest) (jsonRPCResponse, error)
}

func callResourcesList(server string, sender connSendable, lock func(), unlock func(), nextID func() int) ([]ResourceInfo, error) {
	lock()
	id := nextID()
	unlock()
	resp, err := sender.send(jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      &id,
		Method:  "resources/list",
	})
	if err != nil {
		return nil, fmt.Errorf("mcp %s resources/list: %w", server, err)
	}
	if resp.Error != nil {
		// Servers that don't implement resources/* return -32601 (method not found).
		// Treat that as "no resources" rather than a hard error so the caller can
		// happily continue with whatever other servers do support resources.
		if resp.Error.Code == -32601 {
			return nil, nil
		}
		return nil, fmt.Errorf("mcp %s resources/list error %d: %s", server, resp.Error.Code, resp.Error.Message)
	}
	var result resourcesListResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("mcp %s resources/list unmarshal: %w", server, err)
	}
	for i := range result.Resources {
		result.Resources[i].Server = server
	}
	return result.Resources, nil
}

func callResourcesRead(server, uri string, sender connSendable, lock func(), unlock func(), nextID func() int) ([]ResourceContent, error) {
	lock()
	id := nextID()
	unlock()
	resp, err := sender.send(jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      &id,
		Method:  "resources/read",
		Params:  map[string]any{"uri": uri},
	})
	if err != nil {
		return nil, fmt.Errorf("mcp %s resources/read: %w", server, err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("mcp %s resources/read error %d: %s", server, resp.Error.Code, resp.Error.Message)
	}
	var result resourcesReadResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("mcp %s resources/read unmarshal: %w", server, err)
	}
	return result.Contents, nil
}

// ListResources returns every resource exposed by every connected MCP server,
// sorted by server name then URI for stable output. Servers that don't support
// resources/list are silently skipped.
func (m *Manager) ListResources(ctx context.Context) ([]ResourceInfo, error) {
	m.mu.Lock()
	stdio := make(map[string]*serverConn, len(m.servers))
	for name, sc := range m.servers {
		stdio[name] = sc
	}
	sse := make(map[string]*sseConn, len(m.sseConns))
	for name, sc := range m.sseConns {
		sse[name] = sc
	}
	m.mu.Unlock()

	var all []ResourceInfo
	var errs []string
	for name, sc := range stdio {
		res, err := callResourcesList(name, sc, sc.mu.Lock, sc.mu.Unlock, sc.nextID)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		all = append(all, res...)
	}
	for name, sc := range sse {
		res, err := callResourcesList(name, sc, sc.mu.Lock, sc.mu.Unlock, sc.nextID)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		all = append(all, res...)
	}

	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Server != all[j].Server {
			return all[i].Server < all[j].Server
		}
		return all[i].URI < all[j].URI
	})

	if len(errs) > 0 && len(all) == 0 {
		return nil, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return all, nil
}

// ReadResource finds the resource by URI on the given server (or auto-detects
// when serverName is empty by trying every connected server) and returns the
// raw content blocks.
func (m *Manager) ReadResource(ctx context.Context, serverName, uri string) ([]ResourceContent, error) {
	if uri == "" {
		return nil, fmt.Errorf("uri is required")
	}
	m.mu.Lock()
	stdio := make(map[string]*serverConn, len(m.servers))
	for name, sc := range m.servers {
		stdio[name] = sc
	}
	sse := make(map[string]*sseConn, len(m.sseConns))
	for name, sc := range m.sseConns {
		sse[name] = sc
	}
	m.mu.Unlock()

	if serverName != "" {
		if sc, ok := stdio[serverName]; ok {
			return callResourcesRead(serverName, uri, sc, sc.mu.Lock, sc.mu.Unlock, sc.nextID)
		}
		if sc, ok := sse[serverName]; ok {
			return callResourcesRead(serverName, uri, sc, sc.mu.Lock, sc.mu.Unlock, sc.nextID)
		}
		return nil, fmt.Errorf("mcp server %q not connected", serverName)
	}

	// Auto-detect: try each server in turn, return the first success.
	var lastErr error
	for name, sc := range stdio {
		out, err := callResourcesRead(name, uri, sc, sc.mu.Lock, sc.mu.Unlock, sc.nextID)
		if err == nil {
			return out, nil
		}
		lastErr = err
	}
	for name, sc := range sse {
		out, err := callResourcesRead(name, uri, sc, sc.mu.Lock, sc.mu.Unlock, sc.nextID)
		if err == nil {
			return out, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("no MCP server is connected")
}
