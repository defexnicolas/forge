package agent

import (
	"strings"
	"testing"
)

func TestGemmaRawNewlinesInStringLiteral(t *testing.T) {
	// Gemma emits multi-line file contents with literal newlines inside a
	// JSON string value, which is technically invalid JSON. The parser
	// must recover instead of returning "invalid character '\n' in string
	// literal".
	payload := "<tool_call>{\"name\":\"write_file\",\"input\":{\"path\":\"src/Snake.js\",\"text\":\"class SnakeGame {\n    constructor(id) {\n        this.id = id;\n    }\n}\"}}</tool_call>"

	parsed, err := ParseToolCall(payload)
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if !parsed.Found {
		t.Fatal("expected tool call to be found")
	}
	if parsed.Call.Name != "write_file" {
		t.Fatalf("expected name=write_file, got %q", parsed.Call.Name)
	}
	if !strings.Contains(string(parsed.Call.Input), "class SnakeGame") {
		t.Fatalf("expected input to contain file body, got %s", string(parsed.Call.Input))
	}
}

func TestGemmaRawCRLFInStringLiteral(t *testing.T) {
	payload := "<tool_call>{\"name\":\"write_file\",\"input\":{\"path\":\"a.txt\",\"text\":\"line1\r\nline2\"}}</tool_call>"
	parsed, err := ParseToolCall(payload)
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if !parsed.Found || parsed.Call.Name != "write_file" {
		t.Fatalf("expected write_file tool call, got %+v", parsed)
	}
}

func TestEscapedNewlinesStillWork(t *testing.T) {
	// Regression: already-well-escaped JSON must continue to parse identically.
	payload := `<tool_call>{"name":"write_file","input":{"path":"a.txt","text":"line1\nline2"}}</tool_call>`
	parsed, err := ParseToolCall(payload)
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if !parsed.Found || parsed.Call.Name != "write_file" {
		t.Fatalf("expected write_file tool call, got %+v", parsed)
	}
	if !strings.Contains(string(parsed.Call.Input), `line1\nline2`) {
		t.Fatalf("expected escaped \\n preserved, got %s", string(parsed.Call.Input))
	}
}

func TestDetectModelFamily(t *testing.T) {
	cases := map[string]string{
		"google/gemma-4-e4b:2":   "gemma",
		"codegemma-7b":           "codegemma",
		"qwen2.5-coder:32b":      "qwen",
		"Qwen3-Coder-30B-A3B":    "qwen",
		"Qwen3.5-32B-Instruct":   "qwen",
		"meta-llama/Llama-3-8B":  "llama",
		"mistralai/Mixtral-8x7B": "mistral",
		"deepseek-coder":         "deepseek",
		"phi-3-mini":             "phi",
		"some-unknown-model":     "generic",
	}
	for id, want := range cases {
		if got := DetectModelFamily(id); got != want {
			t.Errorf("DetectModelFamily(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestQwenCoderRoutedToFlexibleParser(t *testing.T) {
	reg := DefaultParsers()
	parser := reg.ForModel("Qwen3-Coder-30B-A3B")
	if parser.Name() != "qwen-multi" {
		t.Fatalf("parser = %q, want qwen-multi", parser.Name())
	}

	got, err := parser.Parse(`<tool_call>read_file(path="main.go")</tool_call>`)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Found || got.Call.Name != "read_file" {
		t.Fatalf("expected Qwen coder parser to handle Python-style tool call, got %#v", got)
	}
}

func TestQwen35StillParsesMarkdownJSON(t *testing.T) {
	reg := DefaultParsers()
	parser := reg.ForModel("Qwen3.5-32B-Instruct")

	got, err := parser.Parse("```json\n{\"name\":\"task_list\",\"input\":{}}\n```")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Found || got.Call.Name != "task_list" {
		t.Fatalf("expected Qwen 3.5 parser to handle markdown JSON, got %#v", got)
	}
}

func TestEscapeControlCharsInJSONStrings_OutsideStringsUntouched(t *testing.T) {
	in := "{\n  \"text\": \"a\nb\"\n}"
	out := escapeControlCharsInJSONStrings(in)
	// The newline inside the string value should be escaped; the ones
	// between tokens must stay literal so the JSON structure is preserved.
	if !strings.Contains(out, `"a\nb"`) {
		t.Errorf("expected value newline to be escaped, got %q", out)
	}
	if !strings.Contains(out, "{\n") {
		t.Errorf("expected structural newlines preserved, got %q", out)
	}
}
