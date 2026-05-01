package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// PromptInfo describes one prompt template exposed by an MCP server.
type PromptInfo struct {
	Name        string             `json:"name"`
	Description string             `json:"description,omitempty"`
	Arguments   []PromptArgumentDef `json:"arguments,omitempty"`
	Server      string             `json:"server"`
}

// PromptArgumentDef is a single argument slot a prompt expects.
type PromptArgumentDef struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// PromptMessage is one message emitted by prompts/get; the role/content shape
// matches the MCP spec but stays minimal because forge only renders the text
// content.
type PromptMessage struct {
	Role    string `json:"role"`
	Content struct {
		Type string `json:"type"`
		Text string `json:"text,omitempty"`
	} `json:"content"`
}

// PromptResult is the full response from prompts/get.
type PromptResult struct {
	Description string          `json:"description,omitempty"`
	Messages    []PromptMessage `json:"messages"`
}

type promptsListResult struct {
	Prompts []PromptInfo `json:"prompts"`
}

func callPromptsList(server string, sender connSendable, lock func(), unlock func(), nextID func() int) ([]PromptInfo, error) {
	lock()
	id := nextID()
	unlock()
	resp, err := sender.send(jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      &id,
		Method:  "prompts/list",
	})
	if err != nil {
		return nil, fmt.Errorf("mcp %s prompts/list: %w", server, err)
	}
	if resp.Error != nil {
		// -32601: method not found -- server doesn't expose prompts. That's
		// not an error the caller should propagate, otherwise one capable
		// server poisons the listing for the rest.
		if resp.Error.Code == -32601 {
			return nil, nil
		}
		return nil, fmt.Errorf("mcp %s prompts/list error %d: %s", server, resp.Error.Code, resp.Error.Message)
	}
	var result promptsListResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("mcp %s prompts/list unmarshal: %w", server, err)
	}
	for i := range result.Prompts {
		result.Prompts[i].Server = server
	}
	return result.Prompts, nil
}

func callPromptsGet(server, name string, args map[string]string, sender connSendable, lock func(), unlock func(), nextID func() int) (PromptResult, error) {
	lock()
	id := nextID()
	unlock()
	params := map[string]any{"name": name}
	if len(args) > 0 {
		params["arguments"] = args
	}
	resp, err := sender.send(jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      &id,
		Method:  "prompts/get",
		Params:  params,
	})
	if err != nil {
		return PromptResult{}, fmt.Errorf("mcp %s prompts/get: %w", server, err)
	}
	if resp.Error != nil {
		return PromptResult{}, fmt.Errorf("mcp %s prompts/get error %d: %s", server, resp.Error.Code, resp.Error.Message)
	}
	var result PromptResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return PromptResult{}, fmt.Errorf("mcp %s prompts/get unmarshal: %w", server, err)
	}
	return result, nil
}

// ListPrompts returns prompts from every connected MCP server, sorted by
// (server, name) for stable output.
func (m *Manager) ListPrompts(ctx context.Context) ([]PromptInfo, error) {
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

	var all []PromptInfo
	var errs []string
	for name, sc := range stdio {
		ps, err := callPromptsList(name, sc, sc.mu.Lock, sc.mu.Unlock, sc.nextID)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		all = append(all, ps...)
	}
	for name, sc := range sse {
		ps, err := callPromptsList(name, sc, sc.mu.Lock, sc.mu.Unlock, sc.nextID)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		all = append(all, ps...)
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Server != all[j].Server {
			return all[i].Server < all[j].Server
		}
		return all[i].Name < all[j].Name
	})
	if len(errs) > 0 && len(all) == 0 {
		return nil, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return all, nil
}

// GetPrompt invokes prompts/get on the named server. When server is empty,
// the runtime tries every connected server (first success wins).
func (m *Manager) GetPrompt(ctx context.Context, serverName, name string, args map[string]string) (PromptResult, error) {
	if name == "" {
		return PromptResult{}, fmt.Errorf("prompt name is required")
	}
	m.mu.Lock()
	stdio := make(map[string]*serverConn, len(m.servers))
	for n, sc := range m.servers {
		stdio[n] = sc
	}
	sse := make(map[string]*sseConn, len(m.sseConns))
	for n, sc := range m.sseConns {
		sse[n] = sc
	}
	m.mu.Unlock()

	if serverName != "" {
		if sc, ok := stdio[serverName]; ok {
			return callPromptsGet(serverName, name, args, sc, sc.mu.Lock, sc.mu.Unlock, sc.nextID)
		}
		if sc, ok := sse[serverName]; ok {
			return callPromptsGet(serverName, name, args, sc, sc.mu.Lock, sc.mu.Unlock, sc.nextID)
		}
		return PromptResult{}, fmt.Errorf("mcp server %q not connected", serverName)
	}
	var lastErr error
	for n, sc := range stdio {
		out, err := callPromptsGet(n, name, args, sc, sc.mu.Lock, sc.mu.Unlock, sc.nextID)
		if err == nil {
			return out, nil
		}
		lastErr = err
	}
	for n, sc := range sse {
		out, err := callPromptsGet(n, name, args, sc, sc.mu.Lock, sc.mu.Unlock, sc.nextID)
		if err == nil {
			return out, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return PromptResult{}, lastErr
	}
	return PromptResult{}, fmt.Errorf("no MCP server is connected")
}
