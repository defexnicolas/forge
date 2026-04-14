package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"forge/internal/llm"
)

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

	var call ToolCall
	// Try direct unmarshal.
	if err := json.Unmarshal([]byte(raw), &call); err != nil {
		// Fallback: extract valid JSON by brace matching.
		if extracted := extractJSON(raw); extracted != "" {
			if json.Unmarshal([]byte(extracted), &call) == nil {
				goto parsed
			}
		}
		// Fallback: try to repair truncated JSON.
		if repaired := repairJSON(raw); repaired != "" {
			if json.Unmarshal([]byte(repaired), &call) == nil {
				goto parsed
			}
		}
		return ParsedToolCall{}, fmt.Errorf("invalid tool call JSON: %w", err)
	}
parsed:
	if call.Name == "" {
		return ParsedToolCall{}, fmt.Errorf("tool call missing name")
	}
	if len(call.Input) == 0 || string(call.Input) == "null" {
		call.Input = json.RawMessage(`{}`)
	}
	return ParsedToolCall{
		Before: strings.TrimSpace(content[:start]),
		After:  afterText,
		Call:   call,
		Found:  true,
	}, nil
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
