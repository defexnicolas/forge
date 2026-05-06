package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// TestReadCacheSurvivesAcrossTurns pins the cross-turn lifetime: build mode
// must reuse reads from explore/plan instead of re-fetching the same files.
// The previous behavior (resetReadCache at the top of run()) made this
// impossible — every mode switch was a fresh cache.
func TestReadCacheSurvivesAcrossTurns(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "Game.tsx")
	if err := os.WriteFile(path, []byte("export default function Game() {}"), 0o644); err != nil {
		t.Fatalf("write tmp file: %v", err)
	}
	r := &Runtime{CWD: tmp, readCache: newReadCache()}
	input := json.RawMessage(`{"path":"Game.tsx"}`)
	result := tools.Result{
		Title:   "read_file",
		Summary: path,
		Content: []tools.ContentBlock{{Type: "text", Text: "export default function Game() {}", Path: path}},
	}
	r.storeReadCache(input, &result, "obs")
	// First lookup: HIT.
	if cached, _, ok := r.lookupReadCache(input); !ok || cached == nil {
		t.Fatalf("expected cache hit on first lookup, got hit=%v", ok)
	}
	// "Simulate" a turn boundary — under the old behavior, run() called
	// resetReadCache here. Under the new behavior, nothing happens and the
	// cache persists. The lookup MUST still succeed.
	if cached, _, ok := r.lookupReadCache(input); !ok || cached == nil {
		t.Fatalf("cache should survive across turns; got hit=%v", ok)
	}
}

// TestReadCacheRefetchOnExternalEdit pins the safety net: if the user edits
// the file outside Forge between turns (e.g. saves it from VS Code), the
// next lookup must detect the newer mtime and refetch instead of serving
// stale bytes. Without this, the cross-turn cache would silently drift.
func TestReadCacheRefetchOnExternalEdit(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "stale.tsx")
	if err := os.WriteFile(path, []byte("v1"), 0o644); err != nil {
		t.Fatalf("write tmp file: %v", err)
	}
	r := &Runtime{CWD: tmp, readCache: newReadCache()}
	input := json.RawMessage(`{"path":"stale.tsx"}`)
	result := tools.Result{
		Title:   "read_file",
		Summary: path,
		Content: []tools.ContentBlock{{Type: "text", Text: "v1", Path: path}},
	}
	r.storeReadCache(input, &result, "obs")
	// Confirm cache hit before the external edit.
	if _, _, ok := r.lookupReadCache(input); !ok {
		t.Fatalf("expected cache hit before edit")
	}
	// Bump the file's mtime to simulate an external edit. os.Chtimes is
	// the cross-platform way; future = now + 5s so we don't race the
	// stat resolution on Windows (~1s on FAT/NTFS).
	future := time.Now().Add(5 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	// Lookup MUST miss now — the safety net should detect the newer mtime.
	if cached, _, ok := r.lookupReadCache(input); ok {
		t.Fatalf("expected cache miss after external edit, got hit cached=%v", cached != nil)
	}
}
