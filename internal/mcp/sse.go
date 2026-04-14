package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// sseConn implements an MCP connection over Server-Sent Events (SSE).
// The server exposes an SSE endpoint for server→client messages
// and an HTTP POST endpoint for client→server messages.
type sseConn struct {
	name       string
	baseURL    string // e.g. "http://localhost:3000"
	postURL    string // discovered from SSE endpoint event
	client     *http.Client
	mu         sync.Mutex
	requestID  int
	tools      []mcpToolInfo
	pending    map[int]chan jsonRPCResponse
	cancel     context.CancelFunc
}

func newSSEConn(name, url string) *sseConn {
	return &sseConn{
		name:    name,
		baseURL: strings.TrimRight(url, "/"),
		client:  &http.Client{Timeout: 30 * time.Second},
		pending: map[int]chan jsonRPCResponse{},
	}
}

func (sc *sseConn) nextID() int {
	sc.requestID++
	return sc.requestID
}

// connect opens the SSE stream and starts reading events.
func (sc *sseConn) connect(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	sc.cancel = cancel

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sc.baseURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")

	resp, err := sc.client.Do(req)
	if err != nil {
		return fmt.Errorf("mcp sse %s: connect failed: %w", sc.name, err)
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return fmt.Errorf("mcp sse %s: server returned %d", sc.name, resp.StatusCode)
	}

	go sc.readSSE(resp.Body)
	return nil
}

// readSSE reads the SSE stream and dispatches responses to pending requests.
func (sc *sseConn) readSSE(body io.ReadCloser) {
	defer body.Close()
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var eventType string
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			eventType = ""
			continue
		}
		if strings.HasPrefix(line, "event: ") {
			eventType = strings.TrimPrefix(line, "event: ")
			continue
		}
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			switch eventType {
			case "endpoint":
				// The server tells us where to POST requests.
				sc.mu.Lock()
				if strings.HasPrefix(data, "http") {
					sc.postURL = data
				} else {
					sc.postURL = sc.baseURL + data
				}
				sc.mu.Unlock()
			case "message", "":
				var resp jsonRPCResponse
				if err := json.Unmarshal([]byte(data), &resp); err != nil {
					continue
				}
				if resp.ID != nil {
					sc.mu.Lock()
					ch, ok := sc.pending[*resp.ID]
					if ok {
						delete(sc.pending, *resp.ID)
					}
					sc.mu.Unlock()
					if ok {
						ch <- resp
					}
				}
			}
		}
	}
}

// send sends a JSON-RPC request via HTTP POST and waits for the response via SSE.
func (sc *sseConn) send(req jsonRPCRequest) (jsonRPCResponse, error) {
	sc.mu.Lock()
	postURL := sc.postURL
	sc.mu.Unlock()

	if postURL == "" {
		return jsonRPCResponse{}, fmt.Errorf("mcp sse %s: no POST endpoint discovered yet", sc.name)
	}

	// Register pending response channel.
	ch := make(chan jsonRPCResponse, 1)
	if req.ID != nil {
		sc.mu.Lock()
		sc.pending[*req.ID] = ch
		sc.mu.Unlock()
	}

	data, err := json.Marshal(req)
	if err != nil {
		return jsonRPCResponse{}, err
	}

	httpReq, err := http.NewRequest(http.MethodPost, postURL, bytes.NewReader(data))
	if err != nil {
		return jsonRPCResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := sc.client.Do(httpReq)
	if err != nil {
		return jsonRPCResponse{}, fmt.Errorf("mcp sse %s: POST failed: %w", sc.name, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode >= 400 {
		return jsonRPCResponse{}, fmt.Errorf("mcp sse %s: POST returned %d", sc.name, resp.StatusCode)
	}

	if req.ID == nil {
		return jsonRPCResponse{}, nil
	}

	// Wait for SSE response.
	select {
	case result := <-ch:
		return result, nil
	case <-time.After(30 * time.Second):
		sc.mu.Lock()
		delete(sc.pending, *req.ID)
		sc.mu.Unlock()
		return jsonRPCResponse{}, fmt.Errorf("mcp sse %s: timeout waiting for response", sc.name)
	}
}

func (sc *sseConn) notify(method string, params any) error {
	req := jsonRPCRequest{JSONRPC: "2.0", Method: method, Params: params}
	_, err := sc.send(req)
	return err
}

func (sc *sseConn) close() {
	if sc.cancel != nil {
		sc.cancel()
	}
}
