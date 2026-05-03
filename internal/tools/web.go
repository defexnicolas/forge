package tools

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"forge/internal/config"
	"forge/internal/tools/websearch"
	"golang.org/x/net/html"
)

// ---------- web_fetch ----------

type webFetchTool struct{}

func (webFetchTool) Name() string        { return "web_fetch" }
func (webFetchTool) Description() string { return "Fetch a URL and return its text content." }
func (webFetchTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","required":["url"],"properties":{"url":{"type":"string","description":"The URL to fetch."},"max_length":{"type":"integer","description":"Max characters to return (default 20000)."}}}`)
}
func (webFetchTool) Permission(_ Context, _ json.RawMessage) PermissionRequest {
	return PermissionRequest{Decision: PermissionAsk, Reason: "web_fetch accesses the network"}
}

func (webFetchTool) Run(ctx Context, input json.RawMessage) (Result, error) {
	var req struct {
		URL       string `json:"url"`
		MaxLength int    `json:"max_length"`
	}
	if err := json.Unmarshal(input, &req); err != nil {
		return Result{}, err
	}
	if req.URL == "" {
		return Result{}, fmt.Errorf("url is required")
	}
	if req.MaxLength <= 0 {
		req.MaxLength = 20000
	}

	client := &http.Client{Timeout: 30 * time.Second}
	httpReq, err := http.NewRequestWithContext(ctx.Context, http.MethodGet, req.URL, nil)
	if err != nil {
		return Result{}, err
	}
	httpReq.Header.Set("User-Agent", "forge/0.1")

	resp, err := client.Do(httpReq)
	if err != nil {
		return Result{}, fmt.Errorf("fetch failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(req.MaxLength*2)))
	if err != nil {
		return Result{}, err
	}

	contentType := resp.Header.Get("Content-Type")
	text := string(body)

	// Extract text from HTML.
	if strings.Contains(contentType, "text/html") {
		text = extractTextFromHTML(text)
	}

	if len(text) > req.MaxLength {
		text = text[:req.MaxLength] + "\n[truncated]"
	}

	return Result{
		Title:   "web_fetch",
		Summary: fmt.Sprintf("Fetched %s (%d chars, %s)", req.URL, len(text), resp.Status),
		Content: []ContentBlock{{Type: "text", Text: text}},
	}, nil
}

func extractTextFromHTML(raw string) string {
	doc, err := html.Parse(strings.NewReader(raw))
	if err != nil {
		return raw
	}
	var b strings.Builder
	var extract func(*html.Node)
	extract = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "style", "nav", "footer", "header":
				return
			}
		}
		if n.Type == html.TextNode {
			text := strings.TrimSpace(n.Data)
			if text != "" {
				b.WriteString(text + " ")
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			extract(c)
		}
		if n.Type == html.ElementNode {
			switch n.Data {
			case "p", "div", "br", "li", "h1", "h2", "h3", "h4", "h5", "h6", "tr":
				b.WriteString("\n")
			}
		}
	}
	extract(doc)
	// Clean up whitespace.
	lines := strings.Split(b.String(), "\n")
	var clean []string
	for _, line := range lines {
		line = strings.Join(strings.Fields(line), " ")
		if line != "" {
			clean = append(clean, line)
		}
	}
	return strings.Join(clean, "\n")
}

// ---------- web_search ----------

type webSearchTool struct{}

func (webSearchTool) Name() string        { return "web_search" }
func (webSearchTool) Description() string { return "Search the web (beta) and return parsed results." }
func (webSearchTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","required":["query"],"properties":{"query":{"type":"string","description":"The search query."},"max_results":{"type":"integer","description":"Max results to return (default 5)."}}}`)
}
func (webSearchTool) Permission(_ Context, _ json.RawMessage) PermissionRequest {
	return PermissionRequest{Decision: PermissionAsk, Reason: "web_search accesses the network"}
}

func (webSearchTool) Run(ctx Context, input json.RawMessage) (Result, error) {
	var req struct {
		Query      string `json:"query"`
		MaxResults int    `json:"max_results"`
	}
	if err := json.Unmarshal(input, &req); err != nil {
		return Result{}, err
	}
	if req.Query == "" {
		return Result{}, fmt.Errorf("query is required")
	}
	if req.MaxResults <= 0 {
		req.MaxResults = 5
	}

	backend := selectSearchBackend(ctx.CWD)
	results, err := backend.Search(ctx.Context, req.Query, req.MaxResults)
	if err != nil {
		return Result{}, fmt.Errorf("%s search: %w", backend.Name(), err)
	}
	if len(results) == 0 {
		return Result{
			Title:   "web_search",
			Summary: fmt.Sprintf("No results (%s) for: %s", backend.Name(), req.Query),
			Content: []ContentBlock{{Type: "text", Text: "No results found."}},
		}, nil
	}

	var b strings.Builder
	for i, r := range results {
		fmt.Fprintf(&b, "%d. %s\n   %s\n   %s\n\n", i+1, r.Title, r.URL, r.Snippet)
	}
	return Result{
		Title:   "web_search",
		Summary: fmt.Sprintf("%d results (%s) for: %s", len(results), backend.Name(), req.Query),
		Content: []ContentBlock{{Type: "text", Text: strings.TrimSpace(b.String())}},
	}, nil
}

// selectSearchBackend reads the effective workspace+global config and
// returns the configured search backend. Falls back to DuckDuckGo when
// nothing is configured (the no-API-key default), so first-run users get
// useful results without setup.
//
// The config load is best-effort: a missing or malformed file defaults to
// DuckDuckGo rather than failing the search.
func selectSearchBackend(cwd string) websearch.Backend {
	cfg, err := config.LoadWithGlobal(cwd)
	if err != nil {
		return websearch.DuckDuckGo{}
	}
	provider := strings.ToLower(strings.TrimSpace(cfg.WebSearch.Provider))
	switch provider {
	case "ollama":
		key := strings.TrimSpace(cfg.WebSearch.APIKey)
		if key == "" {
			if env := strings.TrimSpace(cfg.WebSearch.APIKeyEnv); env != "" {
				key = os.Getenv(env)
			}
		}
		if key == "" {
			key = os.Getenv("OLLAMA_API_KEY")
		}
		return websearch.Ollama{
			BaseURL: cfg.WebSearch.BaseURL,
			APIKey:  key,
		}
	case "", "duckduckgo", "ddg":
		return websearch.DuckDuckGo{}
	default:
		// Unknown provider — log via the result summary by returning DDG;
		// the agent at least gets results instead of an opaque failure.
		return websearch.DuckDuckGo{}
	}
}

// reuseHTMLDeps keeps the html/io/time/http imports referenced after the
// DDG parser moved to internal/tools/websearch. extractTextFromHTML still
// uses html and webFetchTool still uses http/io/time, so this is just
// belt-and-suspenders against an over-eager goimports run.
var (
	_ = html.Parse
	_ = io.LimitReader
	_ = http.MethodGet
	_ = time.Second
)
