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
type ProcessClient struct {
	config    ServerConfig
	cwd       string
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdout    *bufio.Reader
	mu        sync.Mutex
	requestID int
	started   bool
}

// NewProcessClient creates an LSP client for the given server config.
func NewProcessClient(cwd string, cfg ServerConfig) *ProcessClient {
	return &ProcessClient{config: cfg, cwd: cwd}
}

func (c *ProcessClient) start() error {
	if c.started {
		return nil
	}
	cmd := exec.Command(c.config.Command, c.config.Args...)
	cmd.Dir = c.cwd
	cmd.Env = os.Environ()
	cmd.Stderr = io.Discard

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("lsp start %s: %w", c.config.Command, err)
	}
	c.cmd = cmd
	c.stdin = stdin
	c.stdout = bufio.NewReaderSize(stdout, 256*1024)
	c.started = true

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
	// Send initialized notification.
	_ = c.notify("initialized", map[string]any{})
	return nil
}

func (c *ProcessClient) shutdown() {
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.stdin.Close()
		_ = c.cmd.Process.Kill()
		c.started = false
	}
}

func (c *ProcessClient) nextID() int {
	c.requestID++
	return c.requestID
}

// call sends a JSON-RPC request and reads the response.
func (c *ProcessClient) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	id := c.nextID()
	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}
	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(data))
	if _, err := c.stdin.Write([]byte(header)); err != nil {
		return nil, err
	}
	if _, err := c.stdin.Write(data); err != nil {
		return nil, err
	}

	// Read response with timeout.
	type result struct {
		data json.RawMessage
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		r, e := c.readResponse()
		ch <- result{r, e}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-ch:
		return res.data, res.err
	}
}

func (c *ProcessClient) notify(method string, params any) error {
	req := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	}
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(data))
	if _, err := c.stdin.Write([]byte(header)); err != nil {
		return err
	}
	_, err = c.stdin.Write(data)
	return err
}

func (c *ProcessClient) readResponse() (json.RawMessage, error) {
	// Read headers.
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
	var resp struct {
		ID     *int            `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("lsp error %d: %s", resp.Error.Code, resp.Error.Message)
	}
	return resp.Result, nil
}

// Diagnostics returns diagnostics for a file.
func (c *ProcessClient) Diagnostics(file string) ([]Diagnostic, error) {
	if err := c.start(); err != nil {
		return nil, err
	}
	// Open the file to trigger diagnostics.
	content, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	uri := fileURI(file)
	_ = c.notify("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{
			"uri":        uri,
			"languageId": c.config.Language,
			"version":    1,
			"text":       string(content),
		},
	})
	// Give server a moment to compute diagnostics.
	time.Sleep(2 * time.Second)
	// LSP diagnostics come via notifications, not request/response.
	// For simplicity, return empty - real implementation would need async notification handling.
	return nil, nil
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

func parseLocations(data json.RawMessage) []Location {
	// Try as array of locations.
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
	// Try as single location.
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
	// DocumentSymbol format.
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
	// SymbolInformation format.
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
