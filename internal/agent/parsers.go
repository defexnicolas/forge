package agent

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// ToolCallParser defines a strategy for extracting tool calls from model output.
type ToolCallParser interface {
	Name() string
	Parse(content string) (ParsedToolCall, error)
}

// ParserRegistry holds named parsers and selects the best one for a model.
type ParserRegistry struct {
	parsers  []ToolCallParser
	matchers []parserMatcher
}

type parserMatcher struct {
	pattern *regexp.Regexp
	parser  ToolCallParser
}

// DefaultParsers returns the registry with all built-in parsers and model matchers.
func DefaultParsers() *ParserRegistry {
	xml := &xmlTagParser{}
	markdown := &markdownJSONParser{}
	funcCall := &functionCallParser{}
	loose := &looseJSONParser{}

	return &ParserRegistry{
		parsers: []ToolCallParser{xml, markdown, funcCall, loose},
		matchers: []parserMatcher{
			// Gemma models tend to use <tool_call> XML tags correctly.
			{regexp.MustCompile(`(?i)gemma`), xml},
			// Qwen models sometimes use ```json blocks or truncate XML.
			{regexp.MustCompile(`(?i)qwen`), markdown},
			// Llama/Meta models often use function_call style.
			{regexp.MustCompile(`(?i)(llama|meta)`), funcCall},
			// Mistral/Mixtral use <tool_call> XML.
			{regexp.MustCompile(`(?i)(mistral|mixtral)`), xml},
			// DeepSeek uses markdown JSON blocks.
			{regexp.MustCompile(`(?i)deepseek`), markdown},
			// Phi models use various formats.
			{regexp.MustCompile(`(?i)phi`), markdown},
			// CodeGemma and code-specific models.
			{regexp.MustCompile(`(?i)code`), xml},
		},
	}
}

// ForModel returns the best parser for a given model name.
func (r *ParserRegistry) ForModel(model string) ToolCallParser {
	for _, m := range r.matchers {
		if m.pattern.MatchString(model) {
			return m.parser
		}
	}
	return &multiParser{parsers: r.parsers}
}

// ParserNames returns all registered parser names.
func (r *ParserRegistry) ParserNames() []string {
	seen := map[string]bool{}
	var names []string
	for _, p := range r.parsers {
		if !seen[p.Name()] {
			names = append(names, p.Name())
			seen[p.Name()] = true
		}
	}
	return names
}

// ---------- Multi-parser (tries all strategies) ----------

type multiParser struct {
	parsers []ToolCallParser
}

func (p *multiParser) Name() string { return "multi" }
func (p *multiParser) Parse(content string) (ParsedToolCall, error) {
	var lastErr error
	for _, parser := range p.parsers {
		result, err := parser.Parse(content)
		if err == nil && result.Found {
			return result, nil
		}
		if err != nil {
			lastErr = err
		}
	}
	// No parser found a tool call — check if it's just plain text.
	if !strings.Contains(content, "tool_call") && !strings.Contains(content, "function_call") &&
		!strings.Contains(content, `"name"`) {
		return ParsedToolCall{}, nil
	}
	if lastErr != nil {
		return ParsedToolCall{}, lastErr
	}
	return ParsedToolCall{}, nil
}

// ---------- XML Tag Parser (<tool_call>JSON</tool_call>) ----------

type xmlTagParser struct{}

func (p *xmlTagParser) Name() string { return "xml-tag" }
func (p *xmlTagParser) Parse(content string) (ParsedToolCall, error) {
	return ParseToolCall(content)
}

// ---------- Markdown JSON Parser (```json ... ```) ----------

type markdownJSONParser struct{}

func (p *markdownJSONParser) Name() string { return "markdown-json" }
func (p *markdownJSONParser) Parse(content string) (ParsedToolCall, error) {
	// First try standard XML tags.
	if result, err := ParseToolCall(content); err == nil && result.Found {
		return result, nil
	}

	// Look for ```json blocks containing tool call JSON.
	patterns := []string{"```json\n", "```\n"}
	for _, prefix := range patterns {
		start := strings.Index(content, prefix)
		if start < 0 {
			continue
		}
		jsonStart := start + len(prefix)
		end := strings.Index(content[jsonStart:], "```")
		var raw string
		if end >= 0 {
			raw = strings.TrimSpace(content[jsonStart : jsonStart+end])
		} else {
			raw = strings.TrimSpace(content[jsonStart:])
		}
		call, err := parseToolCallJSON(raw)
		if err == nil {
			before := strings.TrimSpace(content[:start])
			after := ""
			if end >= 0 {
				after = strings.TrimSpace(content[jsonStart+end+3:])
			}
			return ParsedToolCall{Before: before, After: after, Call: call, Found: true}, nil
		}
	}

	// Try bare JSON object with "name" and "input".
	return tryBareJSON(content)
}

// ---------- Function Call Parser ({"function_call": {"name": ..., "arguments": ...}}) ----------

type functionCallParser struct{}

func (p *functionCallParser) Name() string { return "function-call" }
func (p *functionCallParser) Parse(content string) (ParsedToolCall, error) {
	// First try standard XML tags.
	if result, err := ParseToolCall(content); err == nil && result.Found {
		return result, nil
	}

	// Look for function_call JSON pattern.
	type fcFormat struct {
		FunctionCall struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"function_call"`
	}

	if extracted := extractJSON(content); extracted != "" {
		var fc fcFormat
		if json.Unmarshal([]byte(extracted), &fc) == nil && fc.FunctionCall.Name != "" {
			before := strings.TrimSpace(content[:strings.Index(content, extracted)])
			after := strings.TrimSpace(content[strings.Index(content, extracted)+len(extracted):])
			return ParsedToolCall{
				Before: before,
				After:  after,
				Call: ToolCall{
					Name:  fc.FunctionCall.Name,
					Input: fc.FunctionCall.Arguments,
				},
				Found: true,
			}, nil
		}
	}

	// Also try markdown/bare JSON.
	return tryBareJSON(content)
}

// ---------- Loose JSON Parser (finds any {"name": ..., "input": ...} in text) ----------

type looseJSONParser struct{}

func (p *looseJSONParser) Name() string { return "loose-json" }
func (p *looseJSONParser) Parse(content string) (ParsedToolCall, error) {
	// Try XML first.
	if result, err := ParseToolCall(content); err == nil && result.Found {
		return result, nil
	}
	// Try markdown blocks.
	md := &markdownJSONParser{}
	if result, err := md.Parse(content); err == nil && result.Found {
		return result, nil
	}
	// Try function_call.
	fc := &functionCallParser{}
	if result, err := fc.Parse(content); err == nil && result.Found {
		return result, nil
	}
	// Last resort: find any JSON with "name" key.
	return tryBareJSON(content)
}

// ---------- Helpers ----------

func parseToolCallJSON(raw string) (ToolCall, error) {
	var call ToolCall
	if err := json.Unmarshal([]byte(raw), &call); err != nil {
		if repaired := repairJSON(raw); repaired != "" {
			if json.Unmarshal([]byte(repaired), &call) == nil && call.Name != "" {
				return normalizeCall(call), nil
			}
		}
		return ToolCall{}, err
	}
	if call.Name == "" {
		return ToolCall{}, fmt.Errorf("tool call missing name")
	}
	return normalizeCall(call), nil
}

func tryBareJSON(content string) (ParsedToolCall, error) {
	extracted := extractJSON(content)
	if extracted == "" {
		return ParsedToolCall{}, nil
	}
	call, err := parseToolCallJSON(extracted)
	if err != nil {
		return ParsedToolCall{}, nil // not an error — just no tool call found
	}
	idx := strings.Index(content, extracted)
	before := ""
	after := ""
	if idx >= 0 {
		before = strings.TrimSpace(content[:idx])
		after = strings.TrimSpace(content[idx+len(extracted):])
	}
	return ParsedToolCall{Before: before, After: after, Call: call, Found: true}, nil
}

func normalizeCall(call ToolCall) ToolCall {
	if len(call.Input) == 0 || string(call.Input) == "null" {
		call.Input = json.RawMessage(`{}`)
	}
	return call
}
