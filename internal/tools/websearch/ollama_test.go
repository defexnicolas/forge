package websearch

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOllamaSearchSendsBearerAndDecodesResults(t *testing.T) {
	var capturedAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		var req ollamaSearchRequest
		_ = json.Unmarshal(body, &req)
		if req.Query == "" {
			t.Errorf("query missing in request body: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"title":"Hello","url":"https://example.com","snippet":"A snippet"}]}`))
	}))
	defer srv.Close()

	o := Ollama{BaseURL: srv.URL, APIKey: "secret-token"}
	results, err := o.Search(context.Background(), "anything", 3)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if capturedAuth != "Bearer secret-token" {
		t.Errorf("Authorization header = %q, want 'Bearer secret-token'", capturedAuth)
	}
	if len(results) != 1 || results[0].Title != "Hello" || results[0].URL != "https://example.com" || results[0].Snippet != "A snippet" {
		t.Errorf("unexpected results: %#v", results)
	}
}

func TestOllamaSearchRequiresAPIKey(t *testing.T) {
	o := Ollama{}
	_, err := o.Search(context.Background(), "x", 1)
	if err == nil || !strings.Contains(err.Error(), "API key") {
		t.Fatalf("expected missing-API-key error, got %v", err)
	}
}

func TestOllamaSearchSurfacesNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("nope"))
	}))
	defer srv.Close()
	o := Ollama{BaseURL: srv.URL, APIKey: "k"}
	_, err := o.Search(context.Background(), "q", 1)
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("expected 401 error, got %v", err)
	}
}
