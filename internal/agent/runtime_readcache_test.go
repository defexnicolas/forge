package agent

import (
	"strings"
	"testing"

	"forge/internal/tools"
)

func TestAnnotateRereadResultPrependsNote(t *testing.T) {
	original := tools.Result{
		Title:   "Read file",
		Summary: "src/Game.tsx",
		Content: []tools.ContentBlock{{
			Type: "text",
			Text: "package game\n\nfunc Run() {}",
			Path: "/abs/src/Game.tsx",
		}},
	}
	annotated := annotateRereadResult(original, 3)
	if len(annotated.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(annotated.Content))
	}
	got := annotated.Content[0].Text
	if !strings.HasPrefix(got, "[NOTE:") {
		t.Errorf("annotated text should start with [NOTE:..., got %q", got[:60])
	}
	if !strings.Contains(got, "3 times") {
		t.Errorf("annotation should reference the serve count, got %q", got)
	}
	if !strings.Contains(got, "package game") {
		t.Errorf("original content must remain accessible after annotation, got %q", got)
	}
	// Defensive copy: mutating annotated must not alter the original.
	if strings.HasPrefix(original.Content[0].Text, "[NOTE:") {
		t.Error("original cached entry was mutated; annotation must be a defensive copy")
	}
}

func TestAnnotateRereadResultSingularGrammar(t *testing.T) {
	original := tools.Result{
		Title:   "Read file",
		Content: []tools.ContentBlock{{Type: "text", Text: "x"}},
	}
	annotated := annotateRereadResult(original, 1)
	got := annotated.Content[0].Text
	if !strings.Contains(got, "1 time ") {
		t.Errorf("singular form 'time' expected for serveCount=1, got %q", got)
	}
	if strings.Contains(got, "1 times") {
		t.Errorf("plural form should not appear for serveCount=1, got %q", got)
	}
}
