package agent

import "testing"

func TestParseToolCall(t *testing.T) {
	parsed, err := ParseToolCall(`thinking <tool_call>{"name":"read_file","input":{"path":"docs/ARCHITECTURE.md"}}</tool_call> done`)
	if err != nil {
		t.Fatal(err)
	}
	if !parsed.Found {
		t.Fatal("expected tool call")
	}
	if parsed.Call.Name != "read_file" {
		t.Fatalf("unexpected tool name %q", parsed.Call.Name)
	}
	if parsed.Before != "thinking" || parsed.After != "done" {
		t.Fatalf("unexpected before/after: %#v", parsed)
	}
}

func TestParseInvalidToolCall(t *testing.T) {
	_, err := ParseToolCall(`<tool_call>{"name":</tool_call>`)
	if err == nil {
		t.Fatal("expected invalid JSON error")
	}
}
