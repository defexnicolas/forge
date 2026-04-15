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
	python := &pythonCallParser{}
	pipe := &pipeToolCallParser{}
	loose := &looseJSONParser{}

	// Gemma flips between three formats: XML tags, Python-style calls, and
	// pipe-delimited <|tool_call>call:NAME{...}<tool_call|>. The multi
	// parser tries all of them so the agent recovers regardless of which
	// one the model picks on a given turn.
	gemma := &multiParser{parsers: []ToolCallParser{xml, pipe, python, markdown, loose}}
	qwen := &multiParser{name: "qwen-multi", parsers: []ToolCallParser{markdown, xml, python, pipe, funcCall, loose}}

	return &ParserRegistry{
		parsers: []ToolCallParser{xml, pipe, markdown, funcCall, python, loose},
		matchers: []parserMatcher{
			// Gemma 2/3/4 — Python-flavored tool calls, sometimes wrapped in XML.
			{regexp.MustCompile(`(?i)gemma`), gemma},
			// Qwen/Qwen-Coder variants switch between markdown JSON, XML,
			// Python-style tool code, and function_call-shaped JSON.
			{regexp.MustCompile(`(?i)qwen`), qwen},
			// Llama/Meta models often use function_call style.
			{regexp.MustCompile(`(?i)(llama|meta)`), funcCall},
			// Mistral/Mixtral use <tool_call> XML.
			{regexp.MustCompile(`(?i)(mistral|mixtral)`), xml},
			// DeepSeek uses markdown JSON blocks.
			{regexp.MustCompile(`(?i)deepseek`), markdown},
			// Phi models use various formats.
			{regexp.MustCompile(`(?i)phi`), markdown},
			// CodeGemma shares Gemma's quirks.
			{regexp.MustCompile(`(?i)codegemma`), gemma},
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

// DetectModelFamily returns a short family tag for a model id (e.g.
// "gemma", "qwen", "llama", "mistral", "deepseek", "phi"). Falls back to
// "generic" when no known family matches. Used by callers that want to
// surface the resolved family in the UI at model-load time.
func DetectModelFamily(model string) string {
	families := []struct {
		name    string
		pattern *regexp.Regexp
	}{
		{"codegemma", regexp.MustCompile(`(?i)codegemma`)},
		{"gemma", regexp.MustCompile(`(?i)gemma`)},
		{"qwen", regexp.MustCompile(`(?i)qwen`)},
		{"llama", regexp.MustCompile(`(?i)(llama|meta)`)},
		{"mistral", regexp.MustCompile(`(?i)(mistral|mixtral)`)},
		{"deepseek", regexp.MustCompile(`(?i)deepseek`)},
		{"phi", regexp.MustCompile(`(?i)phi`)},
	}
	for _, f := range families {
		if f.pattern.MatchString(model) {
			return f.name
		}
	}
	return "generic"
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
	name    string
	parsers []ToolCallParser
}

func (p *multiParser) Name() string {
	if p.name != "" {
		return p.name
	}
	return "multi"
}
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

// ---------- Pipe-delimited Parser (Gemma alt format) ----------
//
// Handles Gemma's `<|tool_call>call:NAME{JSON}<tool_call|>` variant.
// Some Gemma fine-tunes flip to this instead of the standard XML tags, and
// the body uses a `call:NAME` prefix before the JSON arguments (so the name
// lives outside the JSON object).
//
// Also tolerates variants we've seen in the wild:
//   <|tool_call>NAME{JSON}<|tool_call|>   (no `call:` prefix)
//   <|tool_call>call:NAME{JSON}</tool_call>
//   <tool_call|>call:NAME{JSON}<|tool_call>

var pipeOpenRe = regexp.MustCompile(`<\|?\s*tool_call\s*\|?>`)
var pipeCloseRe = regexp.MustCompile(`<\|?\s*/?\s*tool_call\s*\|?>`)
var pipeCallPrefixRe = regexp.MustCompile(`^(?:call\s*:?\s*)?([A-Za-z_][A-Za-z0-9_]*)\s*(\{[\s\S]*\})\s*$`)

type pipeToolCallParser struct{}

func (p *pipeToolCallParser) Name() string { return "pipe-tool-call" }
func (p *pipeToolCallParser) Parse(content string) (ParsedToolCall, error) {
	// Find the first opening marker.
	openLoc := pipeOpenRe.FindStringIndex(content)
	if openLoc == nil {
		return ParsedToolCall{}, nil
	}
	after := content[openLoc[1]:]
	closeLoc := pipeCloseRe.FindStringIndex(after)
	var body, afterText string
	if closeLoc != nil {
		body = after[:closeLoc[0]]
		afterText = after[closeLoc[1]:]
	} else {
		body = after
	}
	body = strings.TrimSpace(body)
	before := content[:openLoc[0]]

	m := pipeCallPrefixRe.FindStringSubmatch(body)
	if m == nil {
		return ParsedToolCall{}, nil
	}
	name := m[1]
	// Guard: the regex will happily capture `call` as the tool name when the
	// model emits a bare `<tool_call>call {...}</tool_call>` without a real
	// tool name. That produced ghost "call" invocations that failed with
	// "tool not found". Reject and let upstream parsers / reprompts handle it.
	if isPipeNoiseKeyword(name) {
		return ParsedToolCall{}, nil
	}
	rawArgs := m[2]

	args, err := normalizePipeToolInput(rawArgs)
	if err != nil {
		return ParsedToolCall{}, fmt.Errorf("pipe tool_call: invalid args JSON for %s", name)
	}
	return ParsedToolCall{
		Before: strings.TrimSpace(before),
		After:  strings.TrimSpace(afterText),
		Call: ToolCall{
			Name:  name,
			Input: args,
		},
		Found: true,
	}, nil
}

// isPipeNoiseKeyword returns true when the captured "name" is actually one of
// the literal wrapper keywords Gemma leaks (`call`, `tool_call`, `function`,
// `function_call`). These are never real tool names, so treating them as such
// produces ghost invocations that fail with "tool not found".
func isPipeNoiseKeyword(name string) bool {
	switch strings.ToLower(name) {
	case "call", "tool_call", "toolcall", "function", "function_call", "functioncall":
		return true
	}
	return false
}

func normalizePipeToolInput(rawArgs string) (json.RawMessage, error) {
	for _, candidate := range jsonRepairCandidates(rawArgs) {
		if !json.Valid([]byte(candidate)) {
			continue
		}
		args := json.RawMessage(candidate)
		var wrapper struct {
			Input json.RawMessage `json:"input"`
		}
		if json.Unmarshal(args, &wrapper) == nil && len(wrapper.Input) > 0 && string(wrapper.Input) != "null" {
			return wrapper.Input, nil
		}
		return args, nil
	}
	return nil, fmt.Errorf("invalid JSON")
}

func jsonRepairCandidates(raw string) []string {
	seen := map[string]bool{}
	var candidates []string
	add := func(candidate string) {
		if candidate == "" || seen[candidate] {
			return
		}
		seen[candidate] = true
		candidates = append(candidates, candidate)
	}
	add(raw)
	add(sanitizeJSONPunctuation(raw))
	add(escapeControlCharsInJSONStrings(raw))
	for _, candidate := range append([]string{}, candidates...) {
		add(quoteJSONishObjectKeys(candidate))
	}
	for _, candidate := range append([]string{}, candidates...) {
		add(escapeControlCharsInJSONStrings(candidate))
	}
	for _, candidate := range append([]string{}, candidates...) {
		add(repairJSON(candidate))
	}
	for _, candidate := range append([]string{}, candidates...) {
		add(dropArrayLabelPrefixes(candidate))
	}
	return candidates
}

// dropArrayLabelPrefixes strips `"label":` fragments that appear immediately
// before a string element inside a JSON array — a very common malformation
// from small local models that mix object- and array-style enumeration:
//
//   ["a", "label": "b", "c"]  →  ["a", "b", "c"]
//
// It is intentionally conservative: only applied to `"ident":"...` patterns
// where the previous non-whitespace character is a comma or `[`, so prose
// inside string values is not rewritten.
func dropArrayLabelPrefixes(s string) string {
	if !strings.ContainsAny(s, "[]") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	inStr := false
	escaped := false
	arrDepth := 0
	lastNonSpace := byte(0)
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if inStr {
			b.WriteByte(ch)
			if escaped {
				escaped = false
			} else if ch == '\\' {
				escaped = true
			} else if ch == '"' {
				inStr = false
				lastNonSpace = ch
			}
			continue
		}
		switch ch {
		case '[':
			arrDepth++
			b.WriteByte(ch)
			lastNonSpace = ch
			continue
		case ']':
			if arrDepth > 0 {
				arrDepth--
			}
			b.WriteByte(ch)
			lastNonSpace = ch
			continue
		case '"':
			// Inside an array, after `,` or `[`, check for `"key":` and drop it.
			if arrDepth > 0 && (lastNonSpace == ',' || lastNonSpace == '[') {
				j := i + 1
				for j < len(s) && s[j] != '"' && s[j] != '\n' {
					if s[j] == '\\' {
						j += 2
						continue
					}
					j++
				}
				if j < len(s) && s[j] == '"' {
					k := j + 1
					for k < len(s) && isJSONWhitespace(s[k]) {
						k++
					}
					if k < len(s) && s[k] == ':' {
						// Skip the label and the colon; keep parsing from after ':'.
						i = k
						for i+1 < len(s) && isJSONWhitespace(s[i+1]) {
							i++
						}
						continue
					}
				}
			}
			inStr = true
			b.WriteByte(ch)
			lastNonSpace = ch
			continue
		}
		b.WriteByte(ch)
		if !isJSONWhitespace(ch) {
			lastNonSpace = ch
		}
	}
	return b.String()
}

func quoteJSONishObjectKeys(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 8)
	inStr := false
	escaped := false
	expectKey := false

	for i := 0; i < len(s); {
		ch := s[i]
		if inStr {
			b.WriteByte(ch)
			if escaped {
				escaped = false
			} else if ch == '\\' {
				escaped = true
			} else if ch == '"' {
				inStr = false
			}
			i++
			continue
		}
		switch ch {
		case '"':
			inStr = true
			expectKey = false
			b.WriteByte(ch)
			i++
		case '{', ',':
			expectKey = true
			b.WriteByte(ch)
			i++
		case ' ', '\n', '\r', '\t':
			b.WriteByte(ch)
			i++
		default:
			if expectKey && isIdentStart(ch) {
				j := i + 1
				for j < len(s) && isIdentPart(s[j]) {
					j++
				}
				k := j
				for k < len(s) && isJSONWhitespace(s[k]) {
					k++
				}
				if k < len(s) && s[k] == ':' {
					b.WriteByte('"')
					b.WriteString(s[i:j])
					b.WriteByte('"')
					b.WriteString(s[j:k])
					b.WriteByte(':')
					i = k + 1
					expectKey = false
					continue
				}
			}
			expectKey = false
			b.WriteByte(ch)
			i++
		}
	}
	return b.String()
}

func isIdentStart(ch byte) bool {
	return ch == '_' || (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z')
}

func isIdentPart(ch byte) bool {
	return isIdentStart(ch) || (ch >= '0' && ch <= '9') || ch == '-'
}

func isJSONWhitespace(ch byte) bool {
	return ch == ' ' || ch == '\n' || ch == '\r' || ch == '\t'
}

// ---------- Python Call Parser (func_name(k=v, k2=v2)) ----------
//
// Handles Gemma's native "tool_code" output where the model emits real Python
// syntax instead of JSON, e.g.:
//
//	<tool_call>
//	write_file(path="index.html", content="<html>...</html>")
//	</tool_call>
//
// or inside a ```tool_code block. We parse the identifier, then walk kwargs
// with proper string/quote/paren tracking so escaped quotes and nested
// parens in content don't break us.

type pythonCallParser struct{}

// pythonCallRe matches `name(args)` on a single line or spread across lines.
// Capture 1 = function name, capture 2 = args body.
var pythonCallRe = regexp.MustCompile(`(?s)([A-Za-z_][A-Za-z0-9_]*)\s*\(([\s\S]*?)\)\s*$`)

func (p *pythonCallParser) Name() string { return "python-call" }
func (p *pythonCallParser) Parse(content string) (ParsedToolCall, error) {
	// Try inside <tool_call> tags first; Gemma wraps Python in them too.
	raw, before, after := peelToolCallTags(content)
	if raw == "" {
		// Fall back to ```tool_code blocks and ```python blocks.
		raw, before, after = peelCodeBlock(content, []string{"```tool_code\n", "```python\n", "```py\n"})
	}
	if raw == "" {
		return ParsedToolCall{}, nil
	}
	raw = strings.TrimSpace(raw)

	// Allow `print(func(...))` wrappers — Gemma loves those. Only strip when
	// both the `print(` prefix and closing `)` are present.
	if strings.HasPrefix(raw, "print(") && strings.HasSuffix(raw, ")") {
		raw = strings.TrimSpace(raw[len("print(") : len(raw)-1])
	}

	name, args, ok := splitPythonCall(raw)
	if !ok {
		return ParsedToolCall{}, nil
	}
	kwargs, err := parsePythonKwargs(args)
	if err != nil {
		return ParsedToolCall{}, err
	}
	inputBytes, err := json.Marshal(kwargs)
	if err != nil {
		return ParsedToolCall{}, err
	}
	return ParsedToolCall{
		Before: strings.TrimSpace(before),
		After:  strings.TrimSpace(after),
		Call: ToolCall{
			Name:  name,
			Input: json.RawMessage(inputBytes),
		},
		Found: true,
	}, nil
}

// splitPythonCall separates `name` from `args` by finding the first top-level
// `(` and matching `)`. Returns the name, the inner args, and ok.
func splitPythonCall(raw string) (name, args string, ok bool) {
	open := strings.IndexByte(raw, '(')
	if open <= 0 {
		return "", "", false
	}
	name = strings.TrimSpace(raw[:open])
	if !isIdentifier(name) {
		return "", "", false
	}
	depth := 0
	inStr := false
	var quote byte
	escaped := false
	for i := open; i < len(raw); i++ {
		ch := raw[i]
		if escaped {
			escaped = false
			continue
		}
		if inStr {
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == quote {
				inStr = false
			}
			continue
		}
		switch ch {
		case '"', '\'':
			inStr = true
			quote = ch
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				args = raw[open+1 : i]
				return name, args, true
			}
		}
	}
	return "", "", false
}

// parsePythonKwargs walks `k1=v1, k2=v2` with quote and paren awareness.
func parsePythonKwargs(body string) (map[string]any, error) {
	out := map[string]any{}
	body = strings.TrimSpace(body)
	if body == "" {
		return out, nil
	}
	pairs := splitTopLevel(body, ',')
	for _, pair := range pairs {
		eq := strings.IndexByte(pair, '=')
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(pair[:eq])
		val := strings.TrimSpace(pair[eq+1:])
		if !isIdentifier(key) {
			return nil, fmt.Errorf("python-call: invalid keyword %q", key)
		}
		parsed, err := parsePythonValue(val)
		if err != nil {
			return nil, err
		}
		out[key] = parsed
	}
	return out, nil
}

func parsePythonValue(v string) (any, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", nil
	}
	switch v {
	case "True", "true":
		return true, nil
	case "False", "false":
		return false, nil
	case "None", "null":
		return nil, nil
	}
	// Triple-quoted Python string -> raw contents.
	for _, q := range []string{`"""`, `'''`} {
		if strings.HasPrefix(v, q) && strings.HasSuffix(v, q) && len(v) >= 2*len(q) {
			return v[len(q) : len(v)-len(q)], nil
		}
	}
	// Standard quoted string (double or single).
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		// Convert single-quoted to JSON-compatible double-quoted and unescape.
		inner := v[1 : len(v)-1]
		if v[0] == '\'' {
			// Re-quote via JSON encoding for safety.
			return inner, nil
		}
		var s string
		if err := json.Unmarshal([]byte(v), &s); err == nil {
			return s, nil
		}
		return inner, nil
	}
	// Numeric.
	if isNumber(v) {
		if strings.Contains(v, ".") {
			var f float64
			if _, err := fmt.Sscan(v, &f); err == nil {
				return f, nil
			}
		} else {
			var n int64
			if _, err := fmt.Sscan(v, &n); err == nil {
				return n, nil
			}
		}
	}
	// Python list / JSON array pass-through.
	if strings.HasPrefix(v, "[") {
		// Replace single-quoted strings inside with double-quoted for JSON.
		jsonish := strings.ReplaceAll(v, "'", `"`)
		var arr any
		if err := json.Unmarshal([]byte(jsonish), &arr); err == nil {
			return arr, nil
		}
	}
	// Dict -> JSON.
	if strings.HasPrefix(v, "{") {
		jsonish := strings.ReplaceAll(v, "'", `"`)
		var m any
		if err := json.Unmarshal([]byte(jsonish), &m); err == nil {
			return m, nil
		}
	}
	// Fallback: treat as literal string.
	return v, nil
}

// splitTopLevel splits by `sep` at depth 0 only (respecting quotes and nested
// parens/brackets/braces).
func splitTopLevel(s string, sep byte) []string {
	var out []string
	var cur strings.Builder
	depth := 0
	inStr := false
	var quote byte
	escaped := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if escaped {
			cur.WriteByte(ch)
			escaped = false
			continue
		}
		if inStr {
			if ch == '\\' {
				cur.WriteByte(ch)
				escaped = true
				continue
			}
			if ch == quote {
				inStr = false
			}
			cur.WriteByte(ch)
			continue
		}
		switch ch {
		case '"', '\'':
			inStr = true
			quote = ch
			cur.WriteByte(ch)
		case '(', '[', '{':
			depth++
			cur.WriteByte(ch)
		case ')', ']', '}':
			depth--
			cur.WriteByte(ch)
		case sep:
			if depth == 0 {
				out = append(out, cur.String())
				cur.Reset()
				continue
			}
			cur.WriteByte(ch)
		default:
			cur.WriteByte(ch)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

func isIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			continue
		}
		if i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

func isNumber(s string) bool {
	if s == "" {
		return false
	}
	seenDot := false
	for i, r := range s {
		if i == 0 && (r == '-' || r == '+') {
			continue
		}
		if r == '.' && !seenDot {
			seenDot = true
			continue
		}
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// peelToolCallTags extracts the payload of <tool_call>...</tool_call>, plus
// text before and after. Returns empty raw if no opening tag.
func peelToolCallTags(content string) (raw, before, after string) {
	start := strings.Index(content, toolCallOpen)
	if start < 0 {
		return "", "", ""
	}
	payloadStart := start + len(toolCallOpen)
	end := strings.Index(content[payloadStart:], toolCallClose)
	if end >= 0 {
		raw = content[payloadStart : payloadStart+end]
		after = content[payloadStart+end+len(toolCallClose):]
	} else {
		raw = content[payloadStart:]
	}
	before = content[:start]
	return raw, before, after
}

// peelCodeBlock finds the first matching code fence and returns its body.
func peelCodeBlock(content string, fences []string) (raw, before, after string) {
	for _, fence := range fences {
		idx := strings.Index(content, fence)
		if idx < 0 {
			continue
		}
		bodyStart := idx + len(fence)
		end := strings.Index(content[bodyStart:], "```")
		if end < 0 {
			return content[bodyStart:], content[:idx], ""
		}
		return content[bodyStart : bodyStart+end], content[:idx], content[bodyStart+end+3:]
	}
	return "", "", ""
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
		for _, candidate := range jsonRepairCandidates(raw) {
			if candidate == raw {
				continue
			}
			if json.Unmarshal([]byte(candidate), &call) == nil && call.Name != "" {
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
