package websearch

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// DuckDuckGo scrapes the html.duckduckgo.com results page. No API key,
// no auth — useful as the always-available default. Schema fragility is
// the trade-off: if DDG changes their HTML, parseDDGResults breaks. We
// keep the parsing tolerant (skip what we can't read) so a layout change
// degrades to "fewer results" rather than a hard error.
type DuckDuckGo struct {
	UserAgent string
}

func (d DuckDuckGo) Name() string { return "duckduckgo" }

func (d DuckDuckGo) Search(ctx context.Context, query string, maxResults int) ([]Result, error) {
	if maxResults <= 0 {
		maxResults = 5
	}
	searchURL := "https://html.duckduckgo.com/html/?q=" + url.QueryEscape(query)
	client := &http.Client{Timeout: 15 * time.Second}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, searchURL, nil)
	if err != nil {
		return nil, err
	}
	ua := d.UserAgent
	if ua == "" {
		ua = "forge/0.1"
	}
	httpReq.Header.Set("User-Agent", ua)

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("duckduckgo search failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if err != nil {
		return nil, err
	}
	return parseDDGResults(string(body), maxResults), nil
}

func parseDDGResults(body string, maxResults int) []Result {
	if strings.TrimSpace(body) == "" || maxResults <= 0 {
		return nil
	}
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		return nil
	}
	var results []Result
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
				snippet := ""
				for sib := n.Parent; sib != nil; sib = sib.Parent {
					if getAttr(sib, "class") == "result" || strings.Contains(getAttr(sib, "class"), "result ") {
						snippet = findSnippet(sib)
						break
					}
				}
				if title != "" && href != "" {
					if parsed, err := url.Parse(href); err == nil {
						if actual := parsed.Query().Get("uddg"); actual != "" {
							href = actual
						}
					}
					results = append(results, Result{Title: title, URL: href, Snippet: snippet})
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
			if strings.Contains(class, "result__snippet") || strings.Contains(class, "result-snippet") {
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
