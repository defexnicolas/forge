package lsp

import (
	"fmt"
	"sync"
)

// Router implements Client by dispatching each request to a per-extension
// language server. The first call for a given extension lazily spawns the
// server (NewProcessClient) and caches the connection; subsequent calls
// reuse it. Extensions without a config fall through to a stub that returns
// the canonical "LSP not configured" error so the context builder can surface
// useful messaging in @symbol/@diagnostics.
type Router struct {
	cwd    string
	config Config

	mu      sync.Mutex
	clients map[string]Client // keyed by extension (".go", ".ts", ...)
	stub    Client
}

// NewRouter wires LoadConfig output into a Client implementation. If config
// is empty the router behaves like Stub() for every call. Idempotent: callers
// pass in cwd and the merged config and we lazily spawn servers as files are
// queried.
func NewRouter(cwd string, config Config) *Router {
	return &Router{
		cwd:     cwd,
		config:  config,
		clients: map[string]Client{},
		stub:    Stub(),
	}
}

func (r *Router) clientFor(file string) Client {
	srv, ok := r.config.ResolveForFile(file)
	if !ok {
		return r.stub
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	key := fmt.Sprintf("%s|%s", srv.Command, srv.Language)
	if c, ok := r.clients[key]; ok {
		return c
	}
	c := NewProcessClient(r.cwd, srv)
	r.clients[key] = c
	return c
}

func (r *Router) Diagnostics(file string) ([]Diagnostic, error) {
	return r.clientFor(file).Diagnostics(file)
}

func (r *Router) Definition(file string, line, col int) ([]Location, error) {
	return r.clientFor(file).Definition(file, line, col)
}

func (r *Router) References(file string, line, col int) ([]Location, error) {
	return r.clientFor(file).References(file, line, col)
}

func (r *Router) Symbols(file string) ([]string, error) {
	return r.clientFor(file).Symbols(file)
}

// Shutdown gracefully stops every spawned ProcessClient. Best-effort -- if a
// client doesn't implement a shutdown method (e.g. the stub) it is left
// alone.
func (r *Router) Shutdown() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.clients {
		if pc, ok := c.(*ProcessClient); ok {
			pc.shutdown()
		}
	}
}
