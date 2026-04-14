package yarn

import (
	"strings"
	"testing"
)

func TestUpsertAndSelect(t *testing.T) {
	store := New(t.TempDir())
	if err := store.Upsert(Node{Kind: "mention", Path: "docs/ARCHITECTURE.md", Content: "Forge architecture and YARN context"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert(Node{Kind: "session", Content: "User asked about plugins"}); err != nil {
		t.Fatal(err)
	}

	nodes, err := store.Select("resume architecture", 10000, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) == 0 {
		t.Fatal("expected selected nodes")
	}
	if nodes[0].Path != "docs/ARCHITECTURE.md" {
		t.Fatalf("expected architecture node first, got %#v", nodes[0])
	}
}

func TestUpsertReplacesStableNode(t *testing.T) {
	store := New(t.TempDir())
	if err := store.Upsert(Node{Kind: "instructions", Path: "AGENTS.md", Content: "one"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert(Node{Kind: "instructions", Path: "AGENTS.md", Content: "two"}); err != nil {
		t.Fatal(err)
	}
	nodes, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected one node, got %d", len(nodes))
	}
	if !strings.Contains(nodes[0].Content, "two") {
		t.Fatalf("expected replacement content, got %#v", nodes[0])
	}
}
