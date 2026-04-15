package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"forge/internal/llm"
)

// ErrToolCallValidation is wrapped by ParseToolCall when a tool call parses as
// valid JSON but fails required-field validation. Runtime treats it like a
// parse failure (retry/reprompt), but the reprompt can be more specific about
// what was missing.
var ErrToolCallValidation = errors.New("tool call validation failed")

const (
	toolCallOpen  = "<tool_call>"
	toolCallClose = "</tool_call>"
)

type ToolCall struct {
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type ParsedToolCall struct {
	Before string
	After  string
	Call   ToolCall
	Found  bool
}

func ParseToolCall(content string) (ParsedToolCall, error) {
	start := strings.Index(content, toolCallOpen)
	if start < 0 {
		return ParsedToolCall{}, nil
	}
	payloadStart := start + len(toolCallOpen)
	end := strings.Index(content[payloadStart:], toolCallClose)

	var raw string
	var afterText string
	if end >= 0 {
		payloadEnd := payloadStart + end
		raw = strings.TrimSpace(content[payloadStart:payloadEnd])
		afterText = strings.TrimSpace(content[payloadEnd+len(toolCallClose):])
	} else {
		// No closing tag — model may have truncated. Use everything after <tool_call>.
		raw = strings.TrimSpace(content[payloadStart:])
		afterText = ""
	}

	call, parseErr := unmarshalToolCallWithRepair(raw)
	if parseErr != nil {
		return ParsedToolCall{}, parseErr
	}
	if call.Name == "" {
		return ParsedToolCall{}, fmt.Errorf("tool call missing name")
	}
	if len(call.Input) == 0 || string(call.Input) == "null" {
		call.Input = json.RawMessage(`{}`)
	}
	if err := validateToolArgs(call.Name, call.Input); err != nil {
		return ParsedToolCall{}, fmt.Errorf("%w: %s: %v", ErrToolCallValidation, call.Name, err)
	}
	return ParsedToolCall{
		Before: strings.TrimSpace(content[:start]),
		After:  afterText,
		Call:   call,
		Found:  true,
	}, nil
}

// unmarshalToolCallWithRepair walks the unified repair pipeline (direct →
// sanitize → escape control chars → quote unquoted keys → repair truncation →
// extractJSON) trying to decode a ToolCall. Returns the first successful
// decode; falls back to the original error otherwise.
func unmarshalToolCallWithRepair(raw string) (ToolCall, error) {
	var call ToolCall
	if err := json.Unmarshal([]byte(raw), &call); err == nil {
		return call, nil
	} else {
		lastErr := err
		for _, candidate := range jsonRepairCandidates(raw) {
			if candidate == raw {
				continue
			}
			if json.Unmarshal([]byte(candidate), &call) == nil {
				return call, nil
			}
			if extracted := extractJSON(candidate); extracted != "" {
				if json.Unmarshal([]byte(extracted), &call) == nil {
					return call, nil
				}
			}
		}
		if extracted := extractJSON(raw); extracted != "" {
			if json.Unmarshal([]byte(extracted), &call) == nil {
				return call, nil
			}
		}
		return ToolCall{}, fmt.Errorf("invalid tool call JSON: %w", lastErr)
	}
}

// validateToolArgs checks that a parsed tool call has all required fields
// non-empty for edits that must not execute partially. For unknown tool names
// it returns nil so non-fs tools are not blocked.
func validateToolArgs(name string, rawInput json.RawMessage) error {
	switch name {
	case "edit_file":
		return requireFields(rawInput, []string{"path", "old_text", "new_text"})
	case "write_file":
		// content | text — accept either.
		var args map[string]any
		if err := json.Unmarshal(rawInput, &args); err != nil {
			return fmt.Errorf("arguments not a JSON object: %w", err)
		}
		if !nonEmptyString(args["path"]) {
			return fmt.Errorf("missing required field: path")
		}
		if !nonEmptyString(args["content"]) && !nonEmptyString(args["text"]) {
			return fmt.Errorf("missing required field: content")
		}
		return nil
	case "read_file":
		return requireFields(rawInput, []string{"path"})
	}
	return nil
}

func requireFields(rawInput json.RawMessage, fields []string) error {
	var args map[string]any
	if err := json.Unmarshal(rawInput, &args); err != nil {
		return fmt.Errorf("arguments not a JSON object: %w", err)
	}
	for _, f := range fields {
		if _, ok := args[f]; !ok {
			return fmt.Errorf("missing required field: %s", f)
		}
		if !nonEmptyString(args[f]) {
			return fmt.Errorf("required field is empty: %s", f)
		}
	}
	return nil
}

func nonEmptyString(v any) bool {
	s, ok := v.(string)
	if !ok {
		return false
	}
	return s != ""
}

// repairJSON attempts to fix truncated JSON by closing unclosed strings, objects, and arrays.
func repairJSON(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || s[0] != '{' {
		return ""
	}
	// Count unclosed delimiters.
	inString := false
	escaped := false
	var stack []byte
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' && inString {
			escaped = true
			continue
		}
		if ch == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		switch ch {
		case '{':
			stack = append(stack, '}')
		case '[':
			stack = append(stack, ']')
		case '}', ']':
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	if len(stack) == 0 {
		return s
	}
	// Close unclosed string if needed.
	repaired := s
	if inString {
		repaired += `"`
	}
	// Close unclosed objects/arrays in reverse order.
	for i := len(stack) - 1; i >= 0; i-- {
		repaired += string(stack[i])
	}
	return repaired
}

// sanitizeJSONPunctuation removes Python-ish punctuation that leaks into
// Gemma's JSON output. Specifically: unescaped `(` and `)` that appear
// outside of string literals are stripped. The walker tracks string
// boundaries and escape sequences so characters inside strings are
// preserved exactly.
func sanitizeJSONPunctuation(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inStr := false
	escaped := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if escaped {
			b.WriteByte(ch)
			escaped = false
			continue
		}
		if inStr {
			if ch == '\\' {
				b.WriteByte(ch)
				escaped = true
				continue
			}
			if ch == '"' {
				inStr = false
			}
			b.WriteByte(ch)
			continue
		}
		if ch == '"' {
			inStr = true
			b.WriteByte(ch)
			continue
		}
		// Outside a string: drop stray Python-call parens that have no JSON meaning.
		if ch == '(' || ch == ')' {
			continue
		}
		b.WriteByte(ch)
	}
	return b.String()
}

// escapeControlCharsInJSONStrings rewrites raw \n \r \t that appear inside
// JSON string literals as their escaped forms. Gemma often emits multi-line
// file contents verbatim inside a "text" field, producing JSON that
// json.Unmarshal rejects with "invalid character '\n' in string literal".
// Characters outside of string literals are preserved exactly.
func escapeControlCharsInJSONStrings(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 16)
	inStr := false
	escaped := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if inStr {
			if escaped {
				b.WriteByte(ch)
				escaped = false
				continue
			}
			switch ch {
			case '\\':
				b.WriteByte(ch)
				escaped = true
			case '"':
				b.WriteByte(ch)
				inStr = false
			case '\n':
				b.WriteString(`\n`)
			case '\r':
				b.WriteString(`\r`)
			case '\t':
				b.WriteString(`\t`)
			case '\b':
				b.WriteString(`\b`)
			case '\f':
				b.WriteString(`\f`)
			default:
				b.WriteByte(ch)
			}
			continue
		}
		if ch == '"' {
			inStr = true
		}
		b.WriteByte(ch)
	}
	return b.String()
}

// extractJSON finds the outermost valid JSON object in a string by brace matching.
// Handles strings with unescaped content that breaks naive parsing.
func extractJSON(s string) string {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return ""
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(s); i++ {
		ch := s[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' && inString {
			escaped = true
			continue
		}
		if ch == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		if ch == '{' {
			depth++
		} else if ch == '}' {
			depth--
			if depth == 0 {
				candidate := s[start : i+1]
				// Verify it's valid JSON.
				var test ToolCall
				if json.Unmarshal([]byte(candidate), &test) == nil {
					return candidate
				}
			}
		}
	}
	return ""
}

// buildParseReprompt crafts a targeted user-role reprompt based on the kind
// of parse failure. Validation failures on edit_file get a pointed nudge to
// use apply_patch or smaller exact edits, because re-emitting a huge fragile
// JSON string usually fails the same way twice.
func buildParseReprompt(accumulated string, parseErr error) string {
	lower := strings.ToLower(accumulated)
	if errors.Is(parseErr, ErrToolCallValidation) {
		if strings.Contains(parseErr.Error(), "edit_file") {
			return "Your previous edit_file tool call was missing or truncated a required field (path, old_text, new_text). " +
				"Re-emit a valid tool call. If the edit is large, prefer apply_patch or a smaller exact edit — do NOT re-emit a huge JSON string."
		}
		return "Your previous tool call was missing a required field: " + parseErr.Error() +
			"\nRe-emit exactly one complete <tool_call>{...}</tool_call> with all required fields non-empty."
	}
	if strings.Contains(lower, "edit_file") {
		return "Tool call failed to parse. If you were calling edit_file on a large region, use apply_patch or a smaller exact edit instead of re-emitting a huge JSON string. " +
			"Otherwise, re-emit exactly one complete <tool_call>{...}</tool_call> with valid JSON and no surrounding prose."
	}
	errLower := strings.ToLower(parseErr.Error())
	// The most common small-model failure on plan_write / todo_write is
	// embedding `"key": "value"` entries inside a string array (tasks list).
	// Give a pointed, example-driven nudge so the retry actually lands.
	if strings.Contains(errLower, "after array element") || strings.Contains(errLower, "after top-level value") {
		return "Tool call JSON is invalid: arrays must contain only values separated by commas, not key:value pairs. " +
			"Bad:  [\"a\", \"label\": \"b\"]\n" +
			"Good: [\"a\", \"b\"]\n" +
			"Re-emit exactly one complete <tool_call>{\"name\":\"...\",\"input\":{...}}</tool_call>. " +
			"For plan_write/todo_write, use string arrays (e.g. tasks: [\"step 1\",\"step 2\"]) — no inline objects or colons inside the array."
	}
	if strings.Contains(errLower, "invalid character") || strings.Contains(errLower, "unexpected") {
		return "Tool call JSON is invalid (" + parseErr.Error() + "). " +
			"Common mistakes: trailing commas, unescaped quotes inside strings, key:value pairs inside string arrays, single quotes instead of double quotes. " +
			"Re-emit exactly one complete <tool_call>{\"name\":\"...\",\"input\":{...}}</tool_call> with strict JSON."
	}
	return "Tool call failed to parse. Re-emit exactly one complete <tool_call>{\"name\":\"...\",\"input\":{...}}</tool_call> with valid JSON. Do not include surrounding prose."
}

// containsPartialToolCall returns true when the text contains any
// tool_call-like marker that a parser did NOT recognize as a complete call.
// Callers check this after Parse() returns Found=false to catch
// partial/truncated tags that would otherwise leak into the final answer.
func containsPartialToolCall(s string) bool {
	return strings.Contains(s, "<tool_call") || strings.Contains(s, "</tool_call") ||
		strings.Contains(s, "<|tool_call") || strings.Contains(s, "tool_call|>")
}

// stripPartialToolCallTail trims the earliest `<tool_call`-like marker (and
// anything after it) from accumulated output, so retries-exhausted fallbacks
// don't leak the raw tag into the transcript. More permissive than a plain
// `<tool_call>` match because the leak is usually a malformed/truncated tag.
func stripPartialToolCallTail(s string) string {
	idx := -1
	for _, marker := range []string{"<tool_call", "</tool_call", "<|tool_call", "tool_call|>"} {
		if i := strings.Index(s, marker); i >= 0 && (idx < 0 || i < idx) {
			idx = i
		}
	}
	if idx < 0 {
		return s
	}
	return strings.TrimSpace(s[:idx])
}

// FromNativeToolCall converts an OpenAI-style native ToolCall to the internal ToolCall type.
func FromNativeToolCall(tc llm.ToolCall) ToolCall {
	input := json.RawMessage(tc.Function.Arguments)
	if len(input) == 0 || string(input) == "null" {
		input = json.RawMessage(`{}`)
	}
	return ToolCall{
		Name:  tc.Function.Name,
		Input: input,
	}
}
