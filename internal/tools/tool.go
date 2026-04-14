package tools

import (
	"context"
	"encoding/json"
	"sync"

	"forge/internal/llm"
)

type PermissionDecision string

const (
	PermissionAllow PermissionDecision = "allow"
	PermissionAsk   PermissionDecision = "ask"
	PermissionDeny  PermissionDecision = "deny"
)

type PermissionRequest struct {
	Decision PermissionDecision
	Reason   string
}

type Context struct {
	Context   context.Context
	CWD       string
	SessionID string
	Agent     string
}

type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	Path string `json:"path,omitempty"`
}

type Artifact struct {
	Path        string `json:"path"`
	ContentType string `json:"contentType,omitempty"`
}

type Result struct {
	Title        string         `json:"title"`
	Summary      string         `json:"summary"`
	Content      []ContentBlock `json:"content,omitempty"`
	Artifacts    []Artifact     `json:"artifacts,omitempty"`
	ChangedFiles []string       `json:"changedFiles,omitempty"`
}

type Tool interface {
	Name() string
	Description() string
	Schema() json.RawMessage
	Permission(ctx Context, input json.RawMessage) PermissionRequest
	Run(ctx Context, input json.RawMessage) (Result, error)
}

type StatusProvider interface {
	Status() string
}

type Description struct {
	Name        string          `json:"name"`
	Status      string          `json:"status"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"schema"`
}

type Registry struct {
	mu      sync.RWMutex
	tools   map[string]Tool
	aliases map[string]string
}

func NewRegistry() *Registry {
	return &Registry{
		tools:   map[string]Tool{},
		aliases: map[string]string{},
	}
}

func (r *Registry) Register(tool Tool, aliases ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[tool.Name()] = tool
	for _, alias := range aliases {
		r.aliases[alias] = tool.Name()
	}
}

func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if canonical, ok := r.aliases[name]; ok {
		name = canonical
	}
	tool, ok := r.tools[name]
	return tool, ok
}

func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	return names
}

func (r *Registry) Describe() []Description {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Description, 0, len(r.tools))
	for _, tool := range r.tools {
		out = append(out, Description{
			Name:        tool.Name(),
			Status:      toolStatus(tool),
			Description: tool.Description(),
			Schema:      tool.Schema(),
		})
	}
	return out
}

func toolStatus(tool Tool) string {
	if provider, ok := tool.(StatusProvider); ok {
		status := provider.Status()
		if status != "" {
			return status
		}
	}
	return "ready"
}

// ToolDefs converts registered tools to the OpenAI function-calling format.
// If names is empty, all tools are included.
func (r *Registry) ToolDefs(names []string) []llm.ToolDef {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var selected []Tool
	if len(names) == 0 {
		selected = make([]Tool, 0, len(r.tools))
		for _, t := range r.tools {
			selected = append(selected, t)
		}
	} else {
		selected = make([]Tool, 0, len(names))
		for _, name := range names {
			canonical := name
			if alias, ok := r.aliases[name]; ok {
				canonical = alias
			}
			if t, ok := r.tools[canonical]; ok {
				selected = append(selected, t)
			}
		}
	}

	defs := make([]llm.ToolDef, 0, len(selected))
	for _, t := range selected {
		schema := t.Schema()
		if len(schema) == 0 || string(schema) == "null" {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		defs = append(defs, llm.ToolDef{
			Type: "function",
			Function: llm.FunctionDef{
				Name:        t.Name(),
				Description: t.Description(),
				Parameters:  schema,
			},
		})
	}
	return defs
}
