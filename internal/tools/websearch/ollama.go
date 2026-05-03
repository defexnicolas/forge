package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Ollama hits the Ollama Cloud /api/web_search endpoint. The endpoint is
// authenticated by an API key (ollama.com signup → header
// `Authorization: Bearer <key>`); BaseURL defaults to
// https://ollama.com if not configured.
//
// Note: this is the cloud-hosted search endpoint, not a local Ollama model
// invocation. Local Ollama instances do not expose web_search.
type Ollama struct {
	BaseURL string
	APIKey  string
}

func (o Ollama) Name() string { return "ollama" }

type ollamaSearchRequest struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results,omitempty"`
}

type ollamaSearchResponse struct {
	Results []struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Content string `json:"content"`
		Snippet string `json:"snippet"`
	} `json:"results"`
}

func (o Ollama) Search(ctx context.Context, query string, maxResults int) ([]Result, error) {
	if strings.TrimSpace(o.APIKey) == "" {
		return nil, fmt.Errorf("ollama web_search requires an API key — set it in HUB > Settings > Web Search or via OLLAMA_API_KEY")
	}
	if maxResults <= 0 {
		maxResults = 5
	}
	base := strings.TrimRight(strings.TrimSpace(o.BaseURL), "/")
	if base == "" {
		base = "https://ollama.com"
	}

	body, err := json.Marshal(ollamaSearchRequest{Query: query, MaxResults: maxResults})
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/web_search", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+o.APIKey)
	httpReq.Header.Set("User-Agent", "forge/0.1")

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama web_search request: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("ollama web_search returned %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}
	var decoded ollamaSearchResponse
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return nil, fmt.Errorf("decode ollama web_search response: %w", err)
	}
	results := make([]Result, 0, len(decoded.Results))
	for _, r := range decoded.Results {
		// Prefer snippet, fall back to content (Ollama swaps the field name
		// between API revisions; tolerate both so we don't break on rollouts).
		snippet := strings.TrimSpace(r.Snippet)
		if snippet == "" {
			snippet = strings.TrimSpace(r.Content)
		}
		results = append(results, Result{
			Title:   strings.TrimSpace(r.Title),
			URL:     strings.TrimSpace(r.URL),
			Snippet: snippet,
		})
		if len(results) >= maxResults {
			break
		}
	}
	return results, nil
}
