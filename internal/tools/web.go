package tools

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

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
func (webSearchTool) Description() string { return "Search the web and return results." }
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

	// Use DuckDuckGo HTML search as a free, no-API-key search.
	searchURL := "https://html.duckduckgo.com/html/?q=" + url.QueryEscape(req.Query)
	client := &http.Client{Timeout: 15 * time.Second}
	httpReq, err := http.NewRequestWithContext(ctx.Context, http.MethodGet, searchURL, nil)
	if err != nil {
		return Result{}, err
	}
	httpReq.Header.Set("User-Agent", "forge/0.1")

	resp, err := client.Do(httpReq)
	if err != nil {
		return Result{}, fmt.Errorf("search failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if err != nil {
		return Result{}, err
	}

	results := parseDDGResults(string(body), req.MaxResults)
	if len(results) == 0 {
		return Result{
			Title:   "web_search",
			Summary: "No results found for: " + req.Query,
			Content: []ContentBlock{{Type: "text", Text: "No results found."}},
		}, nil
	}

	var b strings.Builder
	for i, r := range results {
		fmt.Fprintf(&b, "%d. %s\n   %s\n   %s\n\n", i+1, r.title, r.url, r.snippet)
	}

	return Result{
		Title:   "web_search",
		Summary: fmt.Sprintf("%d results for: %s", len(results), req.Query),
		Content: []ContentBlock{{Type: "text", Text: strings.TrimSpace(b.String())}},
	}, nil
}

type searchResult struct {
	title   string
	url     string
	snippet string
}

func parseDDGResults(body string, maxResults int) []searchResult {
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		return nil
	}
	var results []searchResult
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if len(results) >= maxResults {
			return
		}
		if n.Type == html.ElementNode && n.Data == "a" {
			class := getAttr(n, "class")
			if strings.Contains(class, "result__a") {
				href := getAttr(n, "href")
				title := textContent(n)
				// Find sibling snippet.
				snippet := ""
				for sib := n.Parent; sib != nil; sib = sib.Parent {
					if getAttr(sib, "class") == "result" || strings.Contains(getAttr(sib, "class"), "result ") {
						snippet = findSnippet(sib)
						break
					}
				}
				if title != "" && href != "" {
					// DDG wraps URLs in redirect; extract actual URL.
					if parsed, err := url.Parse(href); err == nil {
						if actual := parsed.Query().Get("uddg"); actual != "" {
							href = actual
						}
					}
					results = append(results, searchResult{title: title, url: href, snippet: snippet})
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return results
}

func getAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func textContent(n *html.Node) string {
	if n.Type == html.TextNode {
		return n.Data
	}
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		b.WriteString(textContent(c))
	}
	return strings.TrimSpace(b.String())
}

func findSnippet(n *html.Node) string {
	var walk func(*html.Node) string
	walk = func(node *html.Node) string {
		if node.Type == html.ElementNode {
			class := getAttr(node, "class")
			if strings.Contains(class, "result__snippet") {
				return strings.TrimSpace(textContent(node))
			}
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			if result := walk(c); result != "" {
				return result
			}
		}
		return ""
	}
	return walk(n)
}
