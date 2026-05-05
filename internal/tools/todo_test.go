package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestNormalizeTaskTitleCollapsesEmbeddedNewlines(t *testing.T) {
	in := "Fix gameStore.ts: replace existing store with...attack, defend,\nflee, useItem, pickupItem,\n      usePotion, purchaseItem, rest, openShop)"
	got := normalizeTaskTitle(in)
	if strings.Contains(got, "\n") {
		t.Errorf("newlines should be removed, got %q", got)
	}
	if strings.Contains(got, "  ") {
		t.Errorf("runs of whitespace should be collapsed, got %q", got)
	}
	if !strings.Contains(got, "attack, defend, flee") {
		t.Errorf("content should be preserved across the wrap, got %q", got)
	}
}

func TestNormalizeTaskTitleEmpty(t *testing.T) {
	if got := normalizeTaskTitle("   \n\t  \n"); got != "" {
		t.Errorf("whitespace-only input should yield empty, got %q", got)
	}
}

func TestTodoWriteToolNormalizesItems(t *testing.T) {
	tool := todoWriteTool{}
	input := json.RawMessage(`{"items":["Fix X with multi-line\n      continuation","Run npm run build","   ","Verify Y"]}`)
	res, err := tool.Run(Context{Context: context.Background(), CWD: t.TempDir()}, input)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	text := res.Content[0].Text
	if strings.Contains(text, "      ") {
		t.Errorf("embedded six-space indent should be collapsed, got %q", text)
	}
	if strings.Count(text, "\n") != 2 {
		// Expect 3 surviving items separated by 2 newlines (the
		// whitespace-only entry got dropped).
		t.Errorf("expected 3 items joined by 2 newlines, got %d newlines in %q", strings.Count(text, "\n"), text)
	}
}
