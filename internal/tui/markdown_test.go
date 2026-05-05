package tui

import (
	"strings"
	"testing"
)

func TestTailRunesShorter(t *testing.T) {
	if got := tailRunes("hi", 10); got != "hi" {
		t.Errorf("tailRunes short = %q, want %q", got, "hi")
	}
}

func TestTailRunesLongerASCII(t *testing.T) {
	got := tailRunes("abcdefghij", 4)
	if got != "…ghij" {
		t.Errorf("tailRunes ASCII = %q, want %q", got, "…ghij")
	}
}

func TestTailRunesUTF8(t *testing.T) {
	// Multi-byte runes — make sure we slice on rune boundaries.
	got := tailRunes("¡hólà mundo!", 5)
	if got != "…undo!" {
		t.Errorf("tailRunes UTF-8 = %q, want '…undo!'", got)
	}
}

func TestFormatStreamingPeekShowsTail(t *testing.T) {
	thinking := strings.Repeat("a", 50) + "FINALWORD"
	raw := "<think>" + thinking
	got := formatStreamingText(raw, false, DefaultTheme())
	if !strings.Contains(got, "FINALWORD") {
		t.Errorf("peek should surface the tail of the in-progress thinking, got %q", got)
	}
	if !strings.Contains(got, "thinking") {
		t.Errorf("peek should include the 'thinking' label, got %q", got)
	}
}

func TestFormatStreamingFullShowsAll(t *testing.T) {
	thinking := "the entire chain of reasoning here"
	raw := "<think>" + thinking + "</think>final"
	got := formatStreamingText(raw, true, DefaultTheme())
	if !strings.Contains(got, thinking) {
		t.Errorf("full mode should include the entire thinking, got %q", got)
	}
	if !strings.Contains(got, "final") {
		t.Errorf("full mode should include post-thinking text, got %q", got)
	}
}

func TestFormatStreamingPeekClosedBlock(t *testing.T) {
	thinking := strings.Repeat("x", 200)
	raw := "<think>" + thinking + "</think>visible answer"
	got := formatStreamingText(raw, false, DefaultTheme())
	if strings.Contains(got, thinking) {
		t.Errorf("peek mode should NOT splice the full thinking text into the output, got %q", got)
	}
	if !strings.Contains(got, "visible answer") {
		t.Errorf("peek mode should pass through post-thinking text, got %q", got)
	}
	if !strings.Contains(got, "thinking") {
		t.Errorf("peek mode should keep a marker for the closed block, got %q", got)
	}
}
