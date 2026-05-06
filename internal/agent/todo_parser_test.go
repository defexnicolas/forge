package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseTodoWriteInputStringArray(t *testing.T) {
	_, items, _, err := parseTodoWriteInput(json.RawMessage(`{"items":["alpha","[x] beta"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0] != "alpha" || items[1] != "[x] beta" {
		t.Fatalf("unexpected items: %#v", items)
	}
}

func TestParseTodoWriteInputObjectArray(t *testing.T) {
	payload := `{"items":[{"id":"plan-1","status":"pending","title":"alpha"},{"title":"beta","status":"completed","notes":"verified"}]}`
	_, items, _, err := parseTodoWriteInput(json.RawMessage(payload))
	if err != nil {
		t.Fatalf("expected lenient parse, got %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d: %#v", len(items), items)
	}
	if items[0] != "alpha" {
		t.Errorf("item 0 should be plain title, got %q", items[0])
	}
	if !strings.HasPrefix(items[1], "[x] ") {
		t.Errorf("item 1 should be marked completed via [x], got %q", items[1])
	}
	if !strings.Contains(items[1], "verified") {
		t.Errorf("item 1 should preserve notes, got %q", items[1])
	}
}

func TestParseTodoWriteInputObjectWithoutTitleSkipped(t *testing.T) {
	_, items, _, err := parseTodoWriteInput(json.RawMessage(`{"items":[{"status":"pending"},{"title":"keeper"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0] != "keeper" {
		t.Fatalf("expected only keeper, got %#v", items)
	}
}

func TestParseTodoWriteInputUnknownShapeIsPrescriptive(t *testing.T) {
	_, _, _, err := parseTodoWriteInput(json.RawMessage(`{"items":[42, true]}`))
	if err == nil {
		t.Fatal("expected error for non-string non-object items")
	}
	msg := err.Error()
	if !strings.Contains(msg, "array of strings") || !strings.Contains(msg, "{title") {
		t.Errorf("error should describe both accepted shapes; got: %s", msg)
	}
}
