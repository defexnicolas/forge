package agent

import (
	"context"
	"fmt"
	"strings"

	"forge/internal/config"
	"forge/internal/llm"
)

// RunBtw fires a parallel "by-the-way" LLM call using the current shared
// context (same system/user framing as the main agent, with the /btw question
// appended). It does NOT participate in the tool loop, does NOT mutate runtime
// state beyond the event stream, and runs in its own goroutine so the main
// agent can continue uninterrupted.
//
// The returned channel emits Event values with Side=true; it is closed after
// the model finishes streaming.
func (r *Runtime) RunBtw(ctx context.Context, question string) <-chan Event {
	events := make(chan Event, 32)
	go r.runBtw(ctx, strings.TrimSpace(question), events)
	return events
}

func (r *Runtime) runBtw(ctx context.Context, question string, events chan<- Event) {
	defer close(events)
	if r == nil || r.Providers == nil {
		events <- Event{Type: EventError, Side: true, Error: fmt.Errorf("btw: runtime not ready")}
		return
	}
	if question == "" {
		events <- Event{Type: EventError, Side: true, Error: fmt.Errorf("btw: empty question")}
		return
	}

	providerName := r.Config.Providers.Default.Name
	provider, ok := r.Providers.Get(providerName)
	if !ok {
		events <- Event{Type: EventError, Side: true, Error: fmt.Errorf("btw: provider %q not registered", providerName)}
		return
	}

	role := "chat"
	cfg := config.ConfigForModelRole(r.Config, role, r.roleModel(role))
	snapshot := r.buildSnapshot(question, cfg)

	model := r.roleModel(role)
	if model == "" {
		model = r.Config.Models["chat"]
	}

	system := "You are answering a short side question while the main agent is still running. " +
		"Be concise (a few sentences). Do NOT call any tools — answer from the context. " +
		"If the context is insufficient, say so plainly.\n\n" + snapshot.Render()

	user := "Side question: " + question

	req := llm.ChatRequest{
		Model: model,
		Messages: []llm.Message{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
	}
	r.applySamplingDefaults(&req)

	stream, err := r.streamProvider(ctx, provider, req)
	if err != nil {
		resp, chatErr := provider.Chat(ctx, req)
		if chatErr != nil {
			events <- Event{Type: EventError, Side: true, Error: fmt.Errorf("btw: %w", chatErr)}
			return
		}
		events <- Event{Type: EventAssistantText, Side: true, Text: resp.Content}
		events <- Event{Type: EventDone, Side: true}
		return
	}

	var text strings.Builder
	for ev := range stream {
		if ev.Error != nil {
			events <- Event{Type: EventError, Side: true, Error: fmt.Errorf("btw stream: %w", ev.Error)}
			continue
		}
		if ev.Text != "" {
			text.WriteString(ev.Text)
			events <- Event{Type: EventAssistantDelta, Side: true, Text: ev.Text}
		}
	}
	final := strings.TrimSpace(text.String())
	if final != "" {
		events <- Event{Type: EventAssistantText, Side: true, Text: final}
	}
	events <- Event{Type: EventDone, Side: true}
}
