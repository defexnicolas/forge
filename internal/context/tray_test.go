package contextbuilder

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTrayPinDrop(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "file.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	tray := NewTray(cwd)
	pin, err := tray.Pin("@file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if pin.Path != "file.txt" {
		t.Fatalf("expected normalized path, got %q", pin.Path)
	}
	if _, err := tray.Pin("file.txt"); err != nil {
		t.Fatal(err)
	}
	pins, err := tray.Pins()
	if err != nil {
		t.Fatal(err)
	}
	if len(pins) != 1 {
		t.Fatalf("expected one pin, got %d", len(pins))
	}
	dropped, err := tray.Drop("file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !dropped {
		t.Fatal("expected pin to be dropped")
	}
	pins, err = tray.Pins()
	if err != nil {
		t.Fatal(err)
	}
	if len(pins) != 0 {
		t.Fatalf("expected no pins, got %d", len(pins))
	}
}

func TestTrayRejectsEscapingPath(t *testing.T) {
	tray := NewTray(t.TempDir())
	if _, err := tray.Pin("../outside.txt"); err == nil {
		t.Fatal("expected escaping path to fail")
	}
}
