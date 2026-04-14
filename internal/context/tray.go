package contextbuilder

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Tray struct {
	cwd  string
	path string
}

type Pin struct {
	Path    string    `json:"path"`
	AddedAt time.Time `json:"added_at"`
}

func NewTray(cwd string) *Tray {
	return &Tray{
		cwd:  cwd,
		path: filepath.Join(cwd, ".forge", "context", "pins.json"),
	}
}

func (t *Tray) Path() string {
	return t.path
}

func (t *Tray) Pin(path string) (Pin, error) {
	normalized, err := t.normalize(path)
	if err != nil {
		return Pin{}, err
	}
	pins, err := t.Pins()
	if err != nil {
		return Pin{}, err
	}
	for _, pin := range pins {
		if pin.Path == normalized {
			return pin, nil
		}
	}
	pin := Pin{Path: normalized, AddedAt: time.Now().UTC()}
	pins = append(pins, pin)
	if err := t.write(pins); err != nil {
		return Pin{}, err
	}
	return pin, nil
}

func (t *Tray) Drop(path string) (bool, error) {
	normalized, err := t.normalize(path)
	if err != nil {
		return false, err
	}
	pins, err := t.Pins()
	if err != nil {
		return false, err
	}
	next := pins[:0]
	dropped := false
	for _, pin := range pins {
		if pin.Path == normalized {
			dropped = true
			continue
		}
		next = append(next, pin)
	}
	if !dropped {
		return false, nil
	}
	return true, t.write(next)
}

func (t *Tray) Pins() ([]Pin, error) {
	data, err := os.ReadFile(t.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, nil
	}
	var pins []Pin
	if err := json.Unmarshal(data, &pins); err != nil {
		return nil, err
	}
	return pins, nil
}

func (t *Tray) normalize(path string) (string, error) {
	path = strings.TrimSpace(strings.TrimPrefix(path, "@"))
	if path == "" {
		return "", fmt.Errorf("empty context path")
	}
	resolved, err := workspacePath(t.cwd, path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("pinning directories is not supported yet: %s", path)
	}
	rel, err := filepath.Rel(t.cwd, resolved)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}

func (t *Tray) write(pins []Pin) error {
	if err := os.MkdirAll(filepath.Dir(t.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(pins, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(t.path, data, 0o644)
}
