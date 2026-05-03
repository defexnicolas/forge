// Package websearch defines the pluggable search-backend interface used by
// the web_search tool. Each backend implements Search() to return parsed
// hits regardless of where they came from — DuckDuckGo HTML scrape, Ollama's
// /api/web_search, or any future provider that gets added.
//
// Backends are selected at request time by the web_search tool based on the
// effective config.WebSearch.Provider value, so swapping providers is a
// config edit, not a code change.
package websearch

import "context"

// Result is one hit from any backend. Empty strings are tolerated for
// fields a particular backend does not surface (e.g. Snippet from a search
// API that only returns title+URL).
type Result struct {
	Title   string
	URL     string
	Snippet string
}

// Backend executes a single search and returns up to maxResults hits.
// Implementations should bound their HTTP timeouts internally so a slow
// provider does not stall the agent indefinitely.
type Backend interface {
	Name() string
	Search(ctx context.Context, query string, maxResults int) ([]Result, error)
}
