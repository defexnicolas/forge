package tui

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestRenderQRAsciiProducesScannableMatrix verifies the ASCII fallback
// for the WhatsApp QR phase produces a square-ish block of half-block
// characters when given a plausible pairing token. SSH users rely on
// this — the on-disk PNG is unreachable from a headless terminal.
func TestRenderQRAsciiProducesScannableMatrix(t *testing.T) {
	// Realistic shape: whatsmeow tokens are comma-separated random data.
	// We don't need a real one; any non-empty input exercises the path.
	out := renderQRAscii("ref-token,base64data,fpkey,fpsig")
	if out == "" {
		t.Fatal("non-empty token should produce a QR")
	}
	lines := strings.Split(out, "\n")
	if len(lines) < 10 {
		t.Fatalf("QR should be at least ~10 rows tall, got %d", len(lines))
	}
	width := utf8.RuneCountInString(lines[0])
	for i, line := range lines[1:] {
		if got := utf8.RuneCountInString(line); got != width {
			t.Fatalf("row %d rune width %d, expected %d (matrix must be rectangular)", i+1, got, width)
		}
	}
	// The matrix must contain real pixels — an empty block would be
	// useless. We test that at least one full-block char shows up.
	if !strings.Contains(out, "█") && !strings.Contains(out, "▀") && !strings.Contains(out, "▄") {
		t.Fatal("rendered QR has no block characters; bitmap path is broken")
	}
}

func TestRenderQRAsciiEmptyTokenReturnsEmpty(t *testing.T) {
	if renderQRAscii("") != "" {
		t.Fatal("empty token must produce empty output (caller skips the block)")
	}
	if renderQRAscii("   \t\n") != "" {
		t.Fatal("whitespace-only token must produce empty output")
	}
}
