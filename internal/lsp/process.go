package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ServerConfig describes a language server to launch.
type ServerConfig struct {
	Command  string   `json:"command" toml:"command"`
	Args     []string `json:"args" toml:"args"`
	Language string   `json:"language" toml:"language"` // e.g. "go", "typescript", "python"
}

// ProcessClient implements Client by spawning a language server process.
//
// Compared to the previous implementation, the I/O is split: a single
// readLoop goroutine demuxes everything coming out of the server's stdout
// into either responses (correlated by JSON-RPC id to the call that issued
// them) or notifications (fanned out to handlers, currently just the
// diagnostics cache).
//
// This is what unblocks Diagnostics(): LSP servers publish diagnostics via
// 'textDocument/publishDiagnostics' notifications which the old reader could
// not see (it only ran when Diagnostics held the mutex and only consumed one
// message at a time).
type ProcessClient struct {
	config  ServerConfig
	cwd     string
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  *bufio.Reader
	mu      sync.Mutex
	started bool
	closed  bool

	// Pending in-flight calls: request id -> channel that will receive the
	// raw result bytes (or an error). Owned under mu.
	pending   map[int]chan rpcResponse
	requestID int

	// Diagnostics cache: file URI -> latest published diagnostics. Updated
	// from the read loop, read by Diagnostics().
	diagMu      sync.RWMutex
	diagByURI   map[string][]Diagnostic
	diagWaiters map[string][]chan struct{}
}

type rpcResponse struct {
	result json.RawMessage
	err    error
}

// NewProcessClient creates an LSP client for the given server config.
func NewProcessClient(cwd string, cfg ServerConfig) *ProcessClient {
	return &ProcessClient{
		config:      cfg,
		cwd:         cwd,
		pending:     map[int]chan rpcResponse{},
		diagByURI:   map[string][]Diagnostic{},
		diagWaiters: map[string][]chan struct{}{},
	}
}

func (c *ProcessClient) start() error {
	c.mu.Lock()
	if c.started {
		c.mu.Unlock()
		return nil
	}
	cmd := exec.Command(c.config.Command, c.config.Args...)
	cmd.Dir = c.cwd
	cmd.Env = os.Environ()
	cmd.Stderr = io.Discard

	stdin, err := cmd.StdinPipe()
	if err != nil {
		c.mu.Unlock()
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		c.mu.Unlock()
		return err
	}
	if err := cmd.Start(); err != nil {
		c.mu.Unlock()
		return fmt.Errorf("lsp start %s: %w", c.config.Command, err)
	}
	c.cmd = cmd
	c.stdin = stdin
	c.stdout = bufio.NewReaderSize(stdout, 256*1024)
	c.started = true
	c.mu.Unlock()

	// Pump stdout into the demux. The goroutine exits when the server closes
	// the pipe (server died or shutdown was called).
	go c.readLoop()

	// Initialize handshake.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err = c.call(ctx, "initialize", map[string]any{
		"processId": os.Getpid(),
		"capabilities": map[string]any{
			"textDocument": map[string]any{
				"publishDiagnostics": map[string]any{},
			},
		},
		"rootUri": "file:///" + strings.ReplaceAll(c.cwd, "\\", "/"),
	})
	if err != nil {
		c.shutdown()
		return fmt.Errorf("lsp initialize: %w", err)
	}
	_ = c.notify("initialized", map[string]any{})
	return nil
}

func (c *ProcessClient) shutdown() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.stdin.Close()
		_ = c.cmd.Process.Kill()
	}
	c.started = false
	// Fail any pending callers so they don't block forever.
	for id, ch := range c.pending {
		ch <- rpcResponse{err: fmt.Errorf("lsp client shutting down")}
		delete(c.pending, id)
	}
}

func (c *ProcessClient) nextID() int {
	c.requestID++
	return c.requestID
}

// call sends a JSON-RPC request and blocks until the server replies with a
// matching id (or ctx fires).
func (c *ProcessClient) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, fmt.Errorf("lsp client closed")
	}
	id := c.nextID()
	ch := make(chan rpcResponse, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}
	if err := c.writeMessage(req); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}

	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	case resp := <-ch:
		return resp.result, resp.err
	}
}

func (c *ProcessClient) notify(method string, params any) error {
	req := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	}
	return c.writeMessage(req)
}

func (c *ProcessClient) writeMessage(req map[string]any) error {
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.stdin == nil {
		return fmt.Errorf("lsp client closed")
	}
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(data))
	if _, err := c.stdin.Write([]byte(header)); err != nil {
		return err
	}
	_, err = c.stdin.Write(data)
	return err
}

// readLoop runs in its own goroutine, demuxes responses to pending callers
// and notifications to in-process handlers.
func (c *ProcessClient) readLoop() {
	for {
		body, err := c.readMessage()
		if err != nil {
			// Server closed or stream error -- fail outstanding calls.
			c.mu.Lock()
			for id, ch := range c.pending {
				ch <- rpcResponse{err: err}
				delete(c.pending, id)
			}
			c.mu.Unlock()
			return
		}
		var envelope struct {
			ID     *int            `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			continue
		}
		if envelope.ID != nil && envelope.Method == "" {
			// Response to a call.
			c.mu.Lock()
			ch, ok := c.pending[*envelope.ID]
			if ok {
				delete(c.pending, *envelope.ID)
			}
			c.mu.Unlock()
			if !ok {
				continue
			}
			if envelope.Error != nil {
				ch <- rpcResponse{err: fmt.Errorf("lsp error %d: %s", envelope.Error.Code, envelope.Error.Message)}
			} else {
				ch <- rpcResponse{result: envelope.Result}
			}
			continue
		}
		// Notification (no id, has method) or server-to-client request (id +
		// method, requires reply -- we don't implement those today, just
		// ignore so the server doesn't block).
		if envelope.Method == "textDocument/publishDiagnostics" {
			c.handlePublishDiagnostics(envelope.Params)
		}
	}
}

func (c *ProcessClient) readMessage() ([]byte, error) {
	contentLength := 0
	for {
		line, err := c.stdout.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
		if strings.HasPrefix(line, "Content-Length:") {
			lenStr := strings.TrimSpace(strings.TrimPrefix(line, "Content-Length:"))
			contentLength, _ = strconv.Atoi(lenStr)
		}
	}
	if contentLength == 0 {
		return nil, fmt.Errorf("lsp: missing Content-Length")
	}
	body := make([]byte, contentLength)
	if _, err := io.ReadFull(c.stdout, body); err != nil {
		return nil, err
	}
	return body, nil
}

func (c *ProcessClient) handlePublishDiagnostics(params json.RawMessage) {
	var payload struct {
		URI         string `json:"uri"`
		Diagnostics []struct {
			Range struct {
				Start struct {
					Line      int `json:"line"`
					Character int `json:"character"`
				} `json:"start"`
			} `json:"range"`
			Severity int    `json:"severity"`
			Message  string `json:"message"`
		} `json:"diagnostics"`
	}
	if err := json.Unmarshal(params, &payload); err != nil {
		return
	}
	out := make([]Diagnostic, 0, len(payload.Diagnostics))
	for _, d := range payload.Diagnostics {
		out = append(out, Diagnostic{
			File:     uriToPath(payload.URI),
			Line:     d.Range.Start.Line + 1,
			Severity: severityName(d.Severity),
			Message:  d.Message,
		})
	}
	c.diagMu.Lock()
	c.diagByURI[payload.URI] = out
	waiters := c.diagWaiters[payload.URI]
	delete(c.diagWaiters, payload.URI)
	c.diagMu.Unlock()
	for _, w := range waiters {
		close(w)
	}
}

func severityName(s int) string {
	switch s {
	case 1:
		return "error"
	case 2:
		return "warning"
	case 3:
		return "info"
	case 4:
		return "hint"
	default:
		return "unknown"
	}
}

// Diagnostics opens the file in the server and waits for the next
// publishDiagnostics notification for that URI, up to 5 seconds. If the
// server already cached diagnostics for the URI from a prior open, returns
// them immediately.
func (c *ProcessClient) Diagnostics(file string) ([]Diagnostic, error) {
	if err := c.start(); err != nil {
		return nil, err
	}
	content, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	uri := fileURI(file)

	// Subscribe BEFORE didOpen so we don't race a fast server.
	wait := make(chan struct{})
	c.diagMu.Lock()
	cached, hasCached := c.diagByURI[uri]
	c.diagWaiters[uri] = append(c.diagWaiters[uri], wait)
	c.diagMu.Unlock()

	_ = c.notify("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{
			"uri":        uri,
			"languageId": c.config.Language,
			"version":    1,
			"text":       string(content),
		},
	})

	if hasCached && len(cached) > 0 {
		// We've already got a previous batch. Still wait briefly in case
		// the server republishes for the new didOpen.
	}

	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-wait:
	case <-timer.C:
		// Fall back to whatever we have (possibly stale, possibly empty).
	}

	c.diagMu.RLock()
	diags := append([]Diagnostic(nil), c.diagByURI[uri]...)
	c.diagMu.RUnlock()
	return diags, nil
}

// Definition returns the definition location for a symbol.
func (c *ProcessClient) Definition(file string, line, col int) ([]Location, error) {
	if err := c.start(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := c.call(ctx, "textDocument/definition", map[string]any{
		"textDocument": map[string]any{"uri": fileURI(file)},
		"position":     map[string]any{"line": line, "character": col},
	})
	if err != nil {
		return nil, err
	}
	return parseLocations(result), nil
}

// References returns all references to a symbol.
func (c *ProcessClient) References(file string, line, col int) ([]Location, error) {
	if err := c.start(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := c.call(ctx, "textDocument/references", map[string]any{
		"textDocument": map[string]any{"uri": fileURI(file)},
		"position":     map[string]any{"line": line, "character": col},
		"context":      map[string]any{"includeDeclaration": true},
	})
	if err != nil {
		return nil, err
	}
	return parseLocations(result), nil
}

// Symbols returns document symbols for a file.
func (c *ProcessClient) Symbols(file string) ([]string, error) {
	if err := c.start(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := c.call(ctx, "textDocument/documentSymbol", map[string]any{
		"textDocument": map[string]any{"uri": fileURI(file)},
	})
	if err != nil {
		return nil, err
	}
	return parseSymbolNames(result), nil
}

func fileURI(path string) string {
	path = strings.ReplaceAll(path, "\\", "/")
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return "file://" + path
}

func uriToPath(uri string) string {
	path := strings.TrimPrefix(uri, "file://")
	path = strings.TrimPrefix(path, "/")
	return path
}

func parseLocations(data json.RawMessage) []Location {
	var locs []struct {
		URI   string `json:"uri"`
		Range struct {
			Start struct {
				Line      int `json:"line"`
				Character int `json:"character"`
			} `json:"start"`
		} `json:"range"`
	}
	if json.Unmarshal(data, &locs) == nil && len(locs) > 0 {
		var result []Location
		for _, loc := range locs {
			result = append(result, Location{
				File:  strings.TrimPrefix(loc.URI, "file://"),
				Line:  loc.Range.Start.Line + 1,
				Range: fmt.Sprintf("L%d:%d", loc.Range.Start.Line+1, loc.Range.Start.Character),
			})
		}
		return result
	}
	var single struct {
		URI   string `json:"uri"`
		Range struct {
			Start struct {
				Line      int `json:"line"`
				Character int `json:"character"`
			} `json:"start"`
		} `json:"range"`
	}
	if json.Unmarshal(data, &single) == nil && single.URI != "" {
		return []Location{{
			File:  strings.TrimPrefix(single.URI, "file://"),
			Line:  single.Range.Start.Line + 1,
			Range: fmt.Sprintf("L%d:%d", single.Range.Start.Line+1, single.Range.Start.Character),
		}}
	}
	return nil
}

func parseSymbolNames(data json.RawMessage) []string {
	var docSymbols []struct {
		Name     string `json:"name"`
		Children []struct {
			Name string `json:"name"`
		} `json:"children"`
	}
	if json.Unmarshal(data, &docSymbols) == nil && len(docSymbols) > 0 {
		var names []string
		for _, sym := range docSymbols {
			names = append(names, sym.Name)
			for _, child := range sym.Children {
				names = append(names, "  "+child.Name)
			}
		}
		return names
	}
	var symInfos []struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(data, &symInfos) == nil {
		var names []string
		for _, sym := range symInfos {
			names = append(names, sym.Name)
		}
		return names
	}
	return nil
}
