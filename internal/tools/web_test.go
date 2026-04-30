package tools

import "testing"

func TestParseDDGResultsParsesResultAndSnippet(t *testing.T) {
	body := `
<html><body>
  <div class="result">
    <a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fdoc">Example Result</a>
    <div class="result__snippet">Example snippet text</div>
  </div>
</body></html>`
	results := parseDDGResults(body, 5)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %#v", results)
	}
	if results[0].title != "Example Result" || results[0].url != "https://example.com/doc" || results[0].snippet != "Example snippet text" {
		t.Fatalf("unexpected result: %#v", results[0])
	}
}

func TestParseDDGResultsHandlesEmptyBody(t *testing.T) {
	if results := parseDDGResults("", 5); len(results) != 0 {
		t.Fatalf("expected no results, got %#v", results)
	}
}
