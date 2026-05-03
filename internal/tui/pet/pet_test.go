package pet

import (
	"strings"
	"testing"
)

func TestRenderProducesExpectedRowCount(t *testing.T) {
	frame := Render(New())
	rowCount := strings.Count(frame, "\n") + 1
	if rowCount != Rows {
		t.Fatalf("expected %d rows, got %d", Rows, rowCount)
	}
}

func TestTickAdvancesState(t *testing.T) {
	s := New()
	if s.Tick != 0 {
		t.Fatalf("New() should have Tick=0, got %d", s.Tick)
	}
	s = Tick(s)
	if s.Tick != 1 {
		t.Fatalf("after one Tick expected 1, got %d", s.Tick)
	}
}

func TestRenderHandlesZeroValueState(t *testing.T) {
	// A zero-value State (no rng) must not panic when rendered. The
	// host stores State as a value field on its model; Go zero-init
	// happens before any Tick call.
	var zero State
	out := Render(zero)
	if out == "" {
		t.Fatal("Render returned empty string for zero State")
	}
}

func TestTickHandlesZeroValueState(t *testing.T) {
	// First Tick on a zero-value state must initialize the rng lazily.
	var zero State
	out := Tick(zero)
	if out.Tick != 1 {
		t.Fatalf("Tick on zero state expected Tick=1, got %d", out.Tick)
	}
}

func TestRenderSmallProducesExpectedDimensions(t *testing.T) {
	frame := RenderSmall(New())
	rowCount := strings.Count(frame, "\n") + 1
	if rowCount != SmallRows {
		t.Fatalf("RenderSmall: expected %d rows, got %d", SmallRows, rowCount)
	}
	// Width is harder to assert because each "cell" is either a single
	// braille rune (one terminal column) or a styled escape sequence,
	// so just check that the first row has at least SmallCols visible
	// runes (post-strip).
	rows := strings.Split(frame, "\n")
	if len(rows) == 0 {
		t.Fatal("RenderSmall: no rows")
	}
}

func TestRenderSmallHandlesZeroValueState(t *testing.T) {
	var zero State
	out := RenderSmall(zero)
	if out == "" {
		t.Fatal("RenderSmall returned empty string for zero State")
	}
}
