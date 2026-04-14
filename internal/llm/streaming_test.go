package llm

import (
	"strings"
	"testing"
)

func TestReadSSETextDeltas(t *testing.T) {
	sseData := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"Hello"}}]}`,
		``,
		`data: {"choices":[{"delta":{"content":" world"}}]}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")

	p := &OpenAICompatible{}
	events := make(chan ChatEvent, 10)
	go func() {
		defer close(events)
		p.readSSE(strings.NewReader(sseData), events)
	}()

	var texts []string
	var gotDone bool
	for event := range events {
		switch event.Type {
		case "text":
			texts = append(texts, event.Text)
		case "done":
			gotDone = true
		case "error":
			t.Fatalf("unexpected error: %v", event.Error)
		}
	}

	if !gotDone {
		t.Fatal("expected done event")
	}
	joined := strings.Join(texts, "")
	if joined != "Hello world" {
		t.Fatalf("expected 'Hello world', got %q", joined)
	}
}

func TestReadSSEToolCallDeltas(t *testing.T) {
	sseData := strings.Join([]string{
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read_file","arguments":""}}]}}]}`,
		``,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}}]}`,
		``,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"test.go\"}"}}]}}]}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")

	p := &OpenAICompatible{}
	events := make(chan ChatEvent, 10)
	go func() {
		defer close(events)
		p.readSSE(strings.NewReader(sseData), events)
	}()

	var toolCalls []ToolCall
	for event := range events {
		if event.Type == "tool_calls" {
			toolCalls = event.ToolCalls
		}
	}

	if len(toolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(toolCalls))
	}
	tc := toolCalls[0]
	if tc.ID != "call_1" {
		t.Fatalf("expected id call_1, got %s", tc.ID)
	}
	if tc.Function.Name != "read_file" {
		t.Fatalf("expected name read_file, got %s", tc.Function.Name)
	}
	if tc.Function.Arguments != `{"path":"test.go"}` {
		t.Fatalf("expected arguments, got %s", tc.Function.Arguments)
	}
}

func TestReadSSEMultipleToolCalls(t *testing.T) {
	sseData := strings.Join([]string{
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"a.go\"}"}}]}}]}`,
		``,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_2","type":"function","function":{"name":"list_files","arguments":"{\"path\":\"src\"}"}}]}}]}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")

	p := &OpenAICompatible{}
	events := make(chan ChatEvent, 10)
	go func() {
		defer close(events)
		p.readSSE(strings.NewReader(sseData), events)
	}()

	var toolCalls []ToolCall
	for event := range events {
		if event.Type == "tool_calls" {
			toolCalls = event.ToolCalls
		}
	}

	if len(toolCalls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(toolCalls))
	}
	if toolCalls[0].Function.Name != "read_file" {
		t.Fatalf("expected read_file, got %s", toolCalls[0].Function.Name)
	}
	if toolCalls[1].Function.Name != "list_files" {
		t.Fatalf("expected list_files, got %s", toolCalls[1].Function.Name)
	}
}
