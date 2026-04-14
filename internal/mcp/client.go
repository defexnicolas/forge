package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"forge/internal/tools"
)

// ---------- JSON-RPC types ----------

type jsonRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      *int   `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int            `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ---------- MCP protocol response types ----------

type mcpToolInfo struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type toolsListResult struct {
	Tools []mcpToolInfo `json:"tools"`
}

type mcpContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type toolCallResult struct {
	Content []mcpContentBlock `json:"content"`
	IsError bool              `json:"isError"`
}

// ---------- serverConn ----------

type serverConn struct {
	name      string
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	scanner   *bufio.Scanner
	mu        sync.Mutex
	requestID int
	tools     []mcpToolInfo
}

func (sc *serverConn) nextID() int {
	sc.requestID++
	return sc.requestID
}

// send writes a JSON-RPC request and reads the next JSON-RPC response line.
// Caller must hold sc.mu.
func (sc *serverConn) send(req jsonRPCRequest) (jsonRPCResponse, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return jsonRPCResponse{}, err
	}
	data = append(data, '\n')
	if _, err := sc.stdin.Write(data); err != nil {
		return jsonRPCResponse{}, fmt.Errorf("mcp %s: write failed: %w", sc.name, err)
	}

	// Read lines until we get a JSON-RPC response with a matching id.
	// Notifications (no id) are skipped.
	for {
		if !sc.scanner.Scan() {
			if err := sc.scanner.Err(); err != nil {
				return jsonRPCResponse{}, fmt.Errorf("mcp %s: read failed: %w", sc.name, err)
			}
			return jsonRPCResponse{}, fmt.Errorf("mcp %s: stdout closed", sc.name)
		}
		line := sc.scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		var resp jsonRPCResponse
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			// Skip non-JSON lines (e.g. debug output from server).
			continue
		}
		// Skip notifications (no id).
		if resp.ID == nil {
			continue
		}
		return resp, nil
	}
}

// notify sends a JSON-RPC notification (no id, no response expected).
func (sc *serverConn) notify(method string, params any) error {
	req := jsonRPCRequest{JSONRPC: "2.0", Method: method, Params: params}
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = sc.stdin.Write(data)
	return err
}

// ---------- Manager ----------

// connAdapter wraps either a stdio serverConn or an SSE sseConn behind a common interface.
type connAdapter interface {
	send(req jsonRPCRequest) (jsonRPCResponse, error)
	notify(method string, params any) error
}

// Manager owns all MCP server connections for a workspace.
type Manager struct {
	cwd      string
	registry *tools.Registry
	servers  map[string]*serverConn
	sseConns map[string]*sseConn
	mu       sync.Mutex
}

// NewManager creates a Manager but does not start any servers yet.
func NewManager(cwd string, registry *tools.Registry) *Manager {
	return &Manager{
		cwd:      cwd,
		registry: registry,
		servers:  map[string]*serverConn{},
		sseConns: map[string]*sseConn{},
	}
}

// Start loads .mcp.json, spawns server processes, performs the MCP handshake,
// discovers tools, and registers them in the tool registry.
func (m *Manager) Start(ctx context.Context) error {
	cfg, err := LoadConfig(m.cwd)
	if err != nil {
		return err
	}
	if len(cfg.MCPServers) == 0 {
		return nil
	}

	var errs []string
	for name, sc := range cfg.MCPServers {
		if err := m.startServer(ctx, name, sc); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %s", name, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("mcp server errors:\n%s", strings.Join(errs, "\n"))
	}
	return nil
}

func (m *Manager) startServer(ctx context.Context, name string, cfg ServerConfig) error {
	transport := strings.ToLower(cfg.Transport)
	if transport == "sse" || transport == "http" {
		return m.startSSEServer(ctx, name, cfg)
	}
	if cfg.Command == "" {
		return fmt.Errorf("no command specified")
	}

	args := cfg.Args
	cmd := exec.CommandContext(ctx, cfg.Command, args...)
	cmd.Dir = m.cwd

	// Build environment.
	env := os.Environ()
	for k, v := range cfg.Env {
		env = append(env, k+"="+v)
	}
	cmd.Env = env

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	// Discard stderr so noisy servers don't block.
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start: %w", err)
	}

	sc := &serverConn{
		name:    name,
		cmd:     cmd,
		stdin:   stdinPipe,
		scanner: bufio.NewScanner(stdoutPipe),
	}
	// Allow large JSON lines (up to 4 MB).
	sc.scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	m.mu.Lock()
	m.servers[name] = sc
	m.mu.Unlock()

	// Handshake: initialize.
	if err := m.handshake(sc); err != nil {
		_ = stdinPipe.Close()
		_ = cmd.Process.Kill()
		return fmt.Errorf("handshake failed: %w", err)
	}

	// Discover tools.
	if err := m.discoverTools(sc); err != nil {
		_ = stdinPipe.Close()
		_ = cmd.Process.Kill()
		return fmt.Errorf("tools/list failed: %w", err)
	}

	// Register discovered tools.
	for _, ti := range sc.tools {
		tool := &mcpTool{
			serverName: name,
			info:       ti,
			conn:       sc,
		}
		m.registry.Register(tool)
	}

	return nil
}

func (m *Manager) handshake(sc *serverConn) error {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	id := sc.nextID()
	resp, err := sc.send(jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      &id,
		Method:  "initialize",
		Params: map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":   map[string]any{},
			"clientInfo": map[string]string{
				"name":    "forge",
				"version": "0.1.0",
			},
		},
	})
	if err != nil {
		return err
	}
	if resp.Error != nil {
		return fmt.Errorf("initialize error %d: %s", resp.Error.Code, resp.Error.Message)
	}

	// Send notifications/initialized.
	return sc.notify("notifications/initialized", map[string]any{})
}

func (m *Manager) discoverTools(sc *serverConn) error {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	id := sc.nextID()
	resp, err := sc.send(jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      &id,
		Method:  "tools/list",
	})
	if err != nil {
		return err
	}
	if resp.Error != nil {
		return fmt.Errorf("tools/list error %d: %s", resp.Error.Code, resp.Error.Message)
	}

	var result toolsListResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return fmt.Errorf("tools/list unmarshal: %w", err)
	}
	sc.tools = result.Tools
	return nil
}

// callTool invokes tools/call on the given server connection.
func callTool(sc *serverConn, toolName string, arguments json.RawMessage) (toolCallResult, error) {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	id := sc.nextID()
	resp, err := sc.send(jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      &id,
		Method:  "tools/call",
		Params: map[string]any{
			"name":      toolName,
			"arguments": json.RawMessage(arguments),
		},
	})
	if err != nil {
		return toolCallResult{}, err
	}
	if resp.Error != nil {
		return toolCallResult{}, fmt.Errorf("tools/call error %d: %s", resp.Error.Code, resp.Error.Message)
	}

	var result toolCallResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return toolCallResult{}, fmt.Errorf("tools/call unmarshal: %w", err)
	}
	return result, nil
}

func (m *Manager) startSSEServer(ctx context.Context, name string, cfg ServerConfig) error {
	if cfg.URL == "" {
		return fmt.Errorf("no URL specified for SSE transport")
	}
	conn := newSSEConn(name, cfg.URL)
	if err := conn.connect(ctx); err != nil {
		return err
	}

	// Wait briefly for the endpoint event.
	time.Sleep(500 * time.Millisecond)

	// Handshake.
	conn.mu.Lock()
	id := conn.nextID()
	conn.mu.Unlock()
	resp, err := conn.send(jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      &id,
		Method:  "initialize",
		Params: map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":   map[string]any{},
			"clientInfo":     map[string]string{"name": "forge", "version": "0.1.0"},
		},
	})
	if err != nil {
		conn.close()
		return fmt.Errorf("handshake failed: %w", err)
	}
	if resp.Error != nil {
		conn.close()
		return fmt.Errorf("initialize error %d: %s", resp.Error.Code, resp.Error.Message)
	}
	_ = conn.notify("notifications/initialized", map[string]any{})

	// Discover tools.
	conn.mu.Lock()
	toolsID := conn.nextID()
	conn.mu.Unlock()
	toolsResp, err := conn.send(jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      &toolsID,
		Method:  "tools/list",
	})
	if err != nil {
		conn.close()
		return fmt.Errorf("tools/list failed: %w", err)
	}
	if toolsResp.Error != nil {
		conn.close()
		return fmt.Errorf("tools/list error: %s", toolsResp.Error.Message)
	}
	var result toolsListResult
	if err := json.Unmarshal(toolsResp.Result, &result); err != nil {
		conn.close()
		return fmt.Errorf("tools/list unmarshal: %w", err)
	}
	conn.tools = result.Tools

	m.mu.Lock()
	m.sseConns[name] = conn
	m.mu.Unlock()

	// Register tools.
	for _, ti := range conn.tools {
		tool := &mcpSSETool{serverName: name, info: ti, conn: conn}
		m.registry.Register(tool)
	}
	return nil
}

// mcpSSETool implements tools.Tool for MCP tools over SSE.
type mcpSSETool struct {
	serverName string
	info       mcpToolInfo
	conn       *sseConn
}

func (t *mcpSSETool) Name() string        { return ToolName(t.serverName, t.info.Name) }
func (t *mcpSSETool) Description() string {
	if t.info.Description != "" {
		return t.info.Description
	}
	return "MCP tool from " + t.serverName + " (SSE)"
}
func (t *mcpSSETool) Schema() json.RawMessage {
	if len(t.info.InputSchema) == 0 {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return t.info.InputSchema
}
func (t *mcpSSETool) Permission(_ tools.Context, _ json.RawMessage) tools.PermissionRequest {
	return tools.PermissionRequest{
		Decision: tools.PermissionAsk,
		Reason:   fmt.Sprintf("MCP tool %s on server %s (SSE) requires approval", t.info.Name, t.serverName),
	}
}
func (t *mcpSSETool) Run(_ tools.Context, input json.RawMessage) (tools.Result, error) {
	if len(input) == 0 {
		input = json.RawMessage(`{}`)
	}
	t.conn.mu.Lock()
	id := t.conn.nextID()
	t.conn.mu.Unlock()
	resp, err := t.conn.send(jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      &id,
		Method:  "tools/call",
		Params:  map[string]any{"name": t.info.Name, "arguments": json.RawMessage(input)},
	})
	if err != nil {
		return tools.Result{}, fmt.Errorf("mcp sse %s/%s: %w", t.serverName, t.info.Name, err)
	}
	if resp.Error != nil {
		return tools.Result{}, fmt.Errorf("mcp sse %s/%s error: %s", t.serverName, t.info.Name, resp.Error.Message)
	}
	var callResult toolCallResult
	if err := json.Unmarshal(resp.Result, &callResult); err != nil {
		return tools.Result{}, err
	}
	var content []tools.ContentBlock
	for _, block := range callResult.Content {
		if block.Type == "text" {
			content = append(content, tools.ContentBlock{Type: "text", Text: block.Text})
		}
	}
	summary := "MCP tool completed."
	if callResult.IsError {
		summary = "MCP tool returned an error."
	}
	if len(content) > 0 && content[0].Text != "" {
		s := content[0].Text
		if len(s) > 120 {
			s = s[:120] + "..."
		}
		summary = s
	}
	return tools.Result{Title: t.Name(), Summary: summary, Content: content}, nil
}

// StartFromFile loads an additional .mcp.json file (e.g. from a plugin) and starts its servers.
func (m *Manager) StartFromFile(ctx context.Context, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return err
	}
	var errs []string
	for name, sc := range cfg.MCPServers {
		if err := m.startServer(ctx, name, sc); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %s", name, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("plugin mcp errors:\n%s", strings.Join(errs, "\n"))
	}
	return nil
}

// Shutdown gracefully stops all MCP server processes.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for name, sc := range m.servers {
		_ = sc.stdin.Close()
		done := make(chan struct{})
		go func() {
			_ = sc.cmd.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = sc.cmd.Process.Kill()
		}
		delete(m.servers, name)
	}
	for name, sc := range m.sseConns {
		sc.close()
		delete(m.sseConns, name)
	}
}

// Describe returns a human-readable status of the MCP runtime for the TUI.
func (m *Manager) Describe() string {
	m.mu.Lock()
	defer m.mu.Unlock()

	total := len(m.servers) + len(m.sseConns)
	if total == 0 {
		return "MCP: no servers configured."
	}

	var b strings.Builder
	fmt.Fprintf(&b, "MCP servers: %d\n", total)
	for name, sc := range m.servers {
		fmt.Fprintf(&b, "\n[%s] stdio pid=%d tools=%d", name, sc.cmd.Process.Pid, len(sc.tools))
		for _, t := range sc.tools {
			fmt.Fprintf(&b, "\n  - %s: %s", ToolName(name, t.Name), t.Description)
		}
	}
	for name, sc := range m.sseConns {
		fmt.Fprintf(&b, "\n[%s] sse url=%s tools=%d", name, sc.baseURL, len(sc.tools))
		for _, t := range sc.tools {
			fmt.Fprintf(&b, "\n  - %s: %s", ToolName(name, t.Name), t.Description)
		}
	}
	return b.String()
}

// ToolName returns the canonical forge tool name for an MCP server tool.
func ToolName(serverName, toolName string) string {
	return "mcp_" + serverName + "_" + toolName
}

// ---------- mcpTool implements tools.Tool ----------

type mcpTool struct {
	serverName string
	info       mcpToolInfo
	conn       *serverConn
}

func (t *mcpTool) Name() string {
	return ToolName(t.serverName, t.info.Name)
}

func (t *mcpTool) Description() string {
	desc := t.info.Description
	if desc == "" {
		desc = "MCP tool from " + t.serverName
	}
	return desc
}

func (t *mcpTool) Schema() json.RawMessage {
	if len(t.info.InputSchema) == 0 {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return t.info.InputSchema
}

func (t *mcpTool) Permission(_ tools.Context, _ json.RawMessage) tools.PermissionRequest {
	return tools.PermissionRequest{
		Decision: tools.PermissionAsk,
		Reason:   fmt.Sprintf("MCP tool %s on server %s requires approval", t.info.Name, t.serverName),
	}
}

func (t *mcpTool) Run(_ tools.Context, input json.RawMessage) (tools.Result, error) {
	if len(input) == 0 {
		input = json.RawMessage(`{}`)
	}

	result, err := callTool(t.conn, t.info.Name, input)
	if err != nil {
		return tools.Result{}, fmt.Errorf("mcp %s/%s: %w", t.serverName, t.info.Name, err)
	}

	var content []tools.ContentBlock
	var textParts []string
	for _, block := range result.Content {
		if block.Type == "text" {
			content = append(content, tools.ContentBlock{Type: "text", Text: block.Text})
			textParts = append(textParts, block.Text)
		}
	}

	summary := "MCP tool completed."
	if result.IsError {
		summary = "MCP tool returned an error."
	}
	if len(textParts) > 0 {
		first := textParts[0]
		if len(first) > 120 {
			first = first[:120] + "..."
		}
		summary = first
	}

	return tools.Result{
		Title:   t.Name(),
		Summary: summary,
		Content: content,
	}, nil
}
