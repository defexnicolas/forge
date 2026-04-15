package agent

import (
	"errors"
	"strings"
	"testing"
)

// Truncated edit_file that parses as JSON but is missing a required field
// should fail validation so the runtime can reprompt instead of executing a
// partial edit.
func TestParseToolCall_ValidationRejectsTruncatedEditFile(t *testing.T) {
	content := `<tool_call>{"name":"edit_file","input":{"path":"foo.go","old_text":"bar","new_text":""}}</tool_call>`
	_, err := ParseToolCall(content)
	if err == nil {
		t.Fatal("expected validation error for empty new_text, got nil")
	}
	if !errors.Is(err, ErrToolCallValidation) {
		t.Fatalf("expected ErrToolCallValidation, got %v", err)
	}
}

func TestParseToolCall_ValidationAcceptsCompleteEditFile(t *testing.T) {
	content := `<tool_call>{"name":"edit_file","input":{"path":"foo.go","old_text":"bar","new_text":"baz"}}</tool_call>`
	p, err := ParseToolCall(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !p.Found || p.Call.Name != "edit_file" {
		t.Fatalf("expected edit_file tool call, got %+v", p)
	}
}

// Truncated closing brace should be repaired by the unified repair pipeline.
func TestParseToolCall_RepairsMissingClosingBrace(t *testing.T) {
	content := `<tool_call>{"name":"read_file","input":{"path":"foo.go"}`
	p, err := ParseToolCall(content)
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if p.Call.Name != "read_file" {
		t.Fatalf("expected read_file, got %s", p.Call.Name)
	}
}

// write_file accepts either content or text as its body field.
func TestValidateToolArgs_WriteFileTextAlias(t *testing.T) {
	if err := validateToolArgs("write_file", []byte(`{"path":"a","text":"hello"}`)); err != nil {
		t.Fatalf("text alias should satisfy write_file, got %v", err)
	}
	if err := validateToolArgs("write_file", []byte(`{"path":"a"}`)); err == nil {
		t.Fatal("expected error when both content and text are missing")
	}
}

func TestBuildParseReprompt_EditFileValidation(t *testing.T) {
	content := `<tool_call>{"name":"edit_file","input":{"path":"foo.go","old_text":"bar","new_text":""}}</tool_call>`
	_, err := ParseToolCall(content)
	if err == nil {
		t.Fatal("expected validation error")
	}
	got := buildParseReprompt("...", err)
	if !strings.Contains(got, "apply_patch") {
		t.Fatalf("edit_file validation reprompt should suggest apply_patch, got: %s", got)
	}
}

func TestBuildParseReprompt_GenericParseError(t *testing.T) {
	err := errors.New("invalid character '}' looking for beginning of value")
	got := buildParseReprompt("some prose without any tool markers", err)
	if !strings.Contains(got, "<tool_call>") {
		t.Fatalf("generic reprompt should reference <tool_call> format, got: %s", got)
	}
}

// Gemma sometimes emits <tool_call>call {"id":"plan-1",...}</tool_call> — bare
// `call` keyword, no real tool name. Previous regex happily captured "call" as
// the tool name and executed a ghost invocation that failed with
// "tool not found". Now we reject the captured name so upstream parsers /
// reprompt logic can kick in.
func TestParseToolCall_RejectsBareCallKeyword(t *testing.T) {
	content := `<tool_call>call {"id":"plan-1","status":"in progress"}</tool_call>`
	p, err := ParseToolCall(content)
	if err == nil && p.Found && p.Call.Name == "call" {
		t.Fatalf("bare 'call' keyword should not be accepted as a tool name; got %+v", p)
	}
}

// Ensure a plain valid XML tool call still parses after unification — guards
// against regressions in the repair pipeline refactor.
func TestParseToolCall_ValidXMLStillParses(t *testing.T) {
	content := `<tool_call>{"name":"read_file","input":{"path":"main.go"}}</tool_call>`
	p, err := ParseToolCall(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !p.Found || p.Call.Name != "read_file" {
		t.Fatalf("expected parsed read_file, got %+v", p)
	}
}
