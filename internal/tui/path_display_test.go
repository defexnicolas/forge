package tui

import (
	"path/filepath"
	"testing"
)

func TestCompactDisplayPathFromHome(t *testing.T) {
	home := filepath.Clean(`C:\Users\nicod`)
	path := filepath.Join(home, "Downloads", "letcode")
	got := compactDisplayPathFromHome(path, home)
	want := filepath.Join("~", "Downloads", "letcode")
	if got != want {
		t.Fatalf("compactDisplayPathFromHome(%q) = %q, want %q", path, got, want)
	}
}

func TestCompactDisplayPathFromHomeOutsideHome(t *testing.T) {
	home := filepath.Clean(`C:\Users\nicod`)
	path := filepath.Clean(`D:\work\repo`)
	got := compactDisplayPathFromHome(path, home)
	if got != path {
		t.Fatalf("compactDisplayPathFromHome(%q) = %q, want original path", path, got)
	}
}
