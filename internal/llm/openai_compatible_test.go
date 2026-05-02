package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"forge/internal/config"
)

func TestBuildChatPayloadOmitsSamplingWhenUnset(t *testing.T) {
	payload := buildChatPayload(ChatRequest{
		Model:    "local-model",
		Messages: []Message{{Role: "user", Content: "hello"}},
	}, false)

	if got := payload["model"]; got != "local-model" {
		t.Fatalf("model = %#v, want local-model", got)
	}
	if got := payload["stream"]; got != false {
		t.Fatalf("stream = %#v, want false", got)
	}
	if _, ok := payload["temperature"]; ok {
		t.Fatalf("temperature should be omitted when unset: %#v", payload)
	}
}

func TestBuildChatPayloadIncludesTemperatureWhenExplicit(t *testing.T) {
	temp := 0.6
	payload := buildChatPayload(ChatRequest{
		Model:       "local-model",
		Messages:    []Message{{Role: "user", Content: "hello"}},
		Temperature: &temp,
	}, true)

	got, ok := payload["temperature"]
	if !ok {
		t.Fatalf("temperature missing from payload: %#v", payload)
	}
	if got != temp {
		t.Fatalf("temperature = %#v, want %v", got, temp)
	}
	if payload["stream"] != true {
		t.Fatalf("stream = %#v, want true", payload["stream"])
	}
}

// TestStreamWithIdleFiresWhenProviderGoesSilent simulates a backend that
// emits a first chunk and then stalls. With idle=200ms, the watchdog must
// cancel the request and surface the error as ErrIdleTimeout (so the runtime
// classifies it as a provider timeout and can retry).
func TestStreamWithIdleFiresWhenProviderGoesSilent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Errorf("test server: ResponseWriter is not a Flusher")
			return
		}
		// First chunk arrives quickly so the idle window arms.
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"hi"}}]}`+"\n\n")
		flusher.Flush()
		// Then go silent until the client cancels the connection.
		<-r.Context().Done()
	}))
	defer srv.Close()

	p := NewOpenAICompatible("test", config.ProviderConfig{
		BaseURL:       srv.URL,
		DefaultModel:  "test-model",
		SupportsTools: false,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := p.StreamWithIdle(ctx, ChatRequest{
		Messages: []Message{{Role: "user", Content: "ping"}},
	}, 200*time.Millisecond)
	if err != nil {
		t.Fatalf("StreamWithIdle returned error: %v", err)
	}

	var (
		gotText  bool
		gotError error
	)
	for evt := range stream {
		switch evt.Type {
		case "text":
			gotText = true
		case "error":
			gotError = evt.Error
		}
	}

	if !gotText {
		t.Fatal("expected first text chunk to arrive before idle fired")
	}
	if gotError == nil {
		t.Fatal("expected an error event after idle window expired")
	}
	if !errors.Is(gotError, ErrIdleTimeout) {
		t.Fatalf("expected ErrIdleTimeout, got %T: %v", gotError, gotError)
	}
	if !IsProviderTimeout(gotError) {
		t.Fatalf("ErrIdleTimeout must be classified as a provider timeout, got %v", gotError)
	}
}

// TestStreamWithIdleAllowsSlowFirstChunk verifies the watchdog does not arm
// during the prompt-processing window. The server delays the first chunk by
// longer than the idle interval; the request must succeed.
func TestStreamWithIdleAllowsSlowFirstChunk(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Errorf("test server: ResponseWriter is not a Flusher")
			return
		}
		// Sleep longer than the idle window before any byte is sent.
		select {
		case <-time.After(600 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"ok"}}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	p := NewOpenAICompatible("test", config.ProviderConfig{
		BaseURL:      srv.URL,
		DefaultModel: "test-model",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := p.StreamWithIdle(ctx, ChatRequest{
		Messages: []Message{{Role: "user", Content: "slow"}},
	}, 200*time.Millisecond)
	if err != nil {
		t.Fatalf("StreamWithIdle returned error: %v", err)
	}

	var gotText, gotDone bool
	for evt := range stream {
		switch evt.Type {
		case "text":
			gotText = true
		case "done":
			gotDone = true
		case "error":
			t.Fatalf("idle watchdog must not fire during prompt processing: %v", evt.Error)
		}
	}
	if !gotText || !gotDone {
		t.Fatalf("expected text+done from slow-first-chunk stream (text=%v done=%v)", gotText, gotDone)
	}
}
