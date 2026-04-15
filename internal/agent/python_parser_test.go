package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPythonCallInsideToolCallTags(t *testing.T) {
	p := &pythonCallParser{}
	content := `<tool_call>
write_file(path="index.html", content="<html></html>")
</tool_call>`
	got, err := p.Parse(content)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Found {
		t.Fatal("expected Found=true")
	}
	if got.Call.Name != "write_file" {
		t.Errorf("name = %q", got.Call.Name)
	}
	var args map[string]any
	if err := json.Unmarshal(got.Call.Input, &args); err != nil {
		t.Fatal(err)
	}
	if args["path"] != "index.html" {
		t.Errorf("path = %v", args["path"])
	}
	if !strings.Contains(args["content"].(string), "<html>") {
		t.Errorf("content = %v", args["content"])
	}
}

func TestPythonCallInToolCodeBlock(t *testing.T) {
	p := &pythonCallParser{}
	content := "before text\n```tool_code\nread_file(path=\"main.go\")\n```\nafter text"
	got, err := p.Parse(content)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Found || got.Call.Name != "read_file" {
		t.Fatalf("unexpected %#v", got)
	}
	if got.Before != "before text" {
		t.Errorf("before = %q", got.Before)
	}
	if got.After != "after text" {
		t.Errorf("after = %q", got.After)
	}
}

func TestPythonCallWithPrintWrapper(t *testing.T) {
	p := &pythonCallParser{}
	content := `<tool_call>print(task_list())</tool_call>`
	got, err := p.Parse(content)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Found || got.Call.Name != "task_list" {
		t.Fatalf("unexpected %#v", got)
	}
}

func TestPythonCallWithMultilineString(t *testing.T) {
	p := &pythonCallParser{}
	content := `<tool_call>write_file(path="a.js", content="""line1
line2
line3""")</tool_call>`
	got, err := p.Parse(content)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Found {
		t.Fatal("expected parse")
	}
	var args map[string]any
	_ = json.Unmarshal(got.Call.Input, &args)
	content2 := args["content"].(string)
	if !strings.Contains(content2, "line1") || !strings.Contains(content2, "line3") {
		t.Errorf("content lost lines: %q", content2)
	}
}

func TestGemmaStrayParenBetweenArrayAndObjectClose(t *testing.T) {
	// Exact shape Gemma 3/4 emits: JSON with a rogue ')' leaking from Python-ish
	// dict syntax, between the array close and the object close.
	content := `<tool_call>{"name":"todo_write","input":{"items":["[ ] step 1","[ ] step 2"])}</tool_call>`
	got, err := ParseToolCall(content)
	if err != nil {
		t.Fatalf("expected sanitizer to recover, got %v", err)
	}
	if !got.Found || got.Call.Name != "todo_write" {
		t.Fatalf("unexpected parse: %#v", got)
	}
	var args map[string]any
	if err := json.Unmarshal(got.Call.Input, &args); err != nil {
		t.Fatal(err)
	}
	items, _ := args["items"].([]any)
	if len(items) != 2 {
		t.Errorf("expected 2 items, got %d", len(items))
	}
}

func TestGemmaPipeToolCallFormat(t *testing.T) {
	// Exact payload Gemma 4 emits on some turns.
	content := `<|tool_call>call:task_update{"id":"plan-1","status":"in_progress"}<tool_call|>`
	p := &pipeToolCallParser{}
	got, err := p.Parse(content)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Found {
		t.Fatal("expected pipe parser to match")
	}
	if got.Call.Name != "task_update" {
		t.Errorf("name = %q", got.Call.Name)
	}
	var args map[string]any
	if err := json.Unmarshal(got.Call.Input, &args); err != nil {
		t.Fatal(err)
	}
	if args["id"] != "plan-1" || args["status"] != "in_progress" {
		t.Errorf("args = %#v", args)
	}
}

func TestGemmaPipeToolCallUnwrapsInputWrapper(t *testing.T) {
	content := `<|tool_call>call:write_file{"input":{"path":"snake.js","text":"class SnakeGame {\n  constructor(id) {\n    this.id = id;\n  }\n}"},"tool_call_id":null}<tool_call|>`
	p := &pipeToolCallParser{}
	got, err := p.Parse(content)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Found || got.Call.Name != "write_file" {
		t.Fatalf("unexpected parse: %#v", got)
	}
	var args map[string]any
	if err := json.Unmarshal(got.Call.Input, &args); err != nil {
		t.Fatal(err)
	}
	if args["path"] != "snake.js" {
		t.Fatalf("path = %v", args["path"])
	}
	if _, hasWrapper := args["input"]; hasWrapper {
		t.Fatalf("pipe parser should unwrap input wrapper, got %#v", args)
	}
	if !strings.Contains(args["text"].(string), "SnakeGame") {
		t.Fatalf("text lost file body: %#v", args)
	}
}

func TestGemmaPipeToolCallEscapesRawNewlinesInWrapper(t *testing.T) {
	content := `<|tool_call>call:write_file{"input":{"path":"snake.js","text":"line1
line2"},"tool_call_id":null}<tool_call|>`
	p := &pipeToolCallParser{}
	got, err := p.Parse(content)
	if err != nil {
		t.Fatal(err)
	}
	var args map[string]any
	if err := json.Unmarshal(got.Call.Input, &args); err != nil {
		t.Fatal(err)
	}
	if args["text"] != "line1\nline2" {
		t.Fatalf("text = %#v", args["text"])
	}
}

func TestGemmaPipeToolCallJSONishUnquotedKeys(t *testing.T) {
	content := `<|tool_call>call:read_file{input:{path:"game-loader.js"}}</tool_call>`
	p := &pipeToolCallParser{}
	got, err := p.Parse(content)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Found || got.Call.Name != "read_file" {
		t.Fatalf("unexpected parse: %#v", got)
	}
	var args map[string]any
	if err := json.Unmarshal(got.Call.Input, &args); err != nil {
		t.Fatal(err)
	}
	if args["path"] != "game-loader.js" {
		t.Fatalf("path = %#v, want game-loader.js; raw input=%s", args["path"], string(got.Call.Input))
	}
	if _, hasWrapper := args["input"]; hasWrapper {
		t.Fatalf("expected input wrapper to be unwrapped, got %#v", args)
	}
}

func TestQuoteJSONishObjectKeysPreservesStringContent(t *testing.T) {
	in := `{input:{path:"game-loader.js",text:"do not change input:{path:x}"}}`
	out := quoteJSONishObjectKeys(in)
	if !strings.Contains(out, `"input":{"path":"game-loader.js"`) {
		t.Fatalf("expected object keys quoted, got %s", out)
	}
	if !strings.Contains(out, `input:{path:x}`) {
		t.Fatalf("expected string content preserved, got %s", out)
	}
}

func TestGemmaPipeFormatRoutedViaRegistry(t *testing.T) {
	reg := DefaultParsers()
	parser := reg.ForModel("google/gemma-4-e4b")
	content := `<|tool_call>call:task_list{}<tool_call|>`
	got, err := parser.Parse(content)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Found || got.Call.Name != "task_list" {
		t.Fatalf("expected pipe parser via gemma route, got %#v", got)
	}
}

func TestPipeFormatWithoutCallPrefix(t *testing.T) {
	// Some Gemma fine-tunes omit the `call:` prefix.
	p := &pipeToolCallParser{}
	got, err := p.Parse(`<|tool_call>read_file{"path":"main.go"}<tool_call|>`)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Found || got.Call.Name != "read_file" {
		t.Fatalf("unexpected: %#v", got)
	}
}

func TestSanitizePreservesParensInStrings(t *testing.T) {
	// Parens inside quoted strings must survive sanitation.
	in := `{"name":"x","input":{"note":"(keep this)"}}`
	out := sanitizeJSONPunctuation(in)
	if out != in {
		t.Errorf("sanitizer shouldn't touch parens inside strings:\n  in:  %s\n  out: %s", in, out)
	}
}

func TestGemmaRoutedToMultiParser(t *testing.T) {
	reg := DefaultParsers()
	parser := reg.ForModel("google/gemma-4-e4b")
	// Should successfully parse Python-style call that would break xml-only parser.
	result, err := parser.Parse(`<tool_call>read_file(path="x")</tool_call>`)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Found || result.Call.Name != "read_file" {
		t.Fatalf("expected gemma parser to handle python call, got %#v", result)
	}
}
