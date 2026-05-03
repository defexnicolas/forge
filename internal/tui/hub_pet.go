package tui

import (
	"strings"
	"time"

	"forge/internal/tui/pet"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// petTickMsg is dispatched on the pet animation cadence so the Hub can
// advance the sprite without a goroutine of its own. Wrapping it as a
// distinct type lets shellModel.Update tell pet ticks apart from any
// other tea.Tick consumers in the same loop.
type petTickMsg struct{}

func petTickCmd() tea.Cmd {
	return tea.Tick(pet.TickInterval, func(time.Time) tea.Msg {
		return petTickMsg{}
	})
}

// sidebarHasRoomForPet decides whether the pet should render in the
// sidebar this frame. We require enough vertical room for: 3 header
// lines + at least 6 sidebar items (4 lines of hints) + 4 lines of
// keyboard hints + the pet itself. Below that, hide the pet so the
// hint footer stays reachable instead of overflowing.
func (m shellModel) sidebarHasRoomForPet() bool {
	available := max(1, m.height-4) // matches sidebarView Height(...)
	return available >= pet.Rows+13
}

// sidebarPetLines returns the rendered pet split into individual lines,
// horizontally centered within the sidebar's inner content width.
//
// The sprite ships natively at pet.Cols x pet.Rows (currently 18x6) —
// see docs/pet/convert.py and internal/tui/pet/robot_data.go — so the
// runtime downscaler is gone. PlaceHorizontal does the centering with
// ANSI-aware width math (each braille rune is wrapped in a truecolor
// escape sequence; naive strings.Repeat padding would mis-count the
// visible width).
func (m shellModel) sidebarPetLines() []string {
	if !m.sidebarHasRoomForPet() {
		return nil
	}
	frame := pet.Render(m.pet)
	contentWidth := shellSidebarWidth - 2 - 2 // border + Padding(1,1)
	centered := lipgloss.PlaceHorizontal(contentWidth, lipgloss.Center, frame)
	return strings.Split(centered, "\n")
}
