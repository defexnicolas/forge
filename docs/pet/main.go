package main

import (
	"fmt"
	"math/rand"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ---------------------------------------------------------------------------
// Regiones detectadas en la imagen (en celdas).
// El render base se sobrescribe en estas regiones para animarlas.
// ---------------------------------------------------------------------------

// Ojos: row 8..10, izq col 24..29, der col 36..41
type region struct {
	rowMin, rowMax, colMin, colMax int
}

var (
	leftEye  = region{8, 10, 24, 29}
	rightEye = region{8, 10, 36, 41}
	mouth    = region{15, 16, 26, 39}
	// zona donde aparecen chispas (arriba del robot)
	sparkZone = region{0, 3, 18, 50}
)

// ---------------------------------------------------------------------------
// Frames de animación para los ojos (5 cols x 3 rows de celdas braille).
// Cada string = una fila de runas braille. Los espacios son "transparentes":
// dejan pasar el render base de fondo.
// ---------------------------------------------------------------------------

// Ojos abiertos: arco curvado hacia arriba (sonriente)
var eyeOpen = []string{
	"⣀⠤⠤⣀⡀",
	"      ",
	"      ",
}

// Ojos parpadeando (línea fina)
var eyeBlink = []string{
	"      ",
	"⠒⠒⠒⠒⠒⠒",
	"      ",
}

// Ojos felices/entrecerrados
var eyeHappy = []string{
	"⢀⡠⠤⠤⣀",
	"⠉⠉⠉⠉⠉⠉",
	"      ",
}

// ---------------------------------------------------------------------------
// Estado del programa
// ---------------------------------------------------------------------------

type sparkle struct {
	row, col int
	life     int // ticks que le quedan
	char     string
}

type model struct {
	tick      int
	blinking  bool
	blinkLeft int       // ticks restantes del blink actual
	mouthGlow float64   // 0..1 intensidad cyan de la boca
	sparkles  []sparkle
	width     int
	height    int
	rng       *rand.Rand
}

type tickMsg time.Time

func tickCmd() tea.Cmd {
	return tea.Tick(80*time.Millisecond, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func initialModel() model {
	return model{
		rng: rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

func (m model) Init() tea.Cmd {
	return tickCmd()
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tickMsg:
		m.tick++

		// --- parpadeo: cada ~3-5 segundos
		if m.blinkLeft > 0 {
			m.blinkLeft--
			if m.blinkLeft == 0 {
				m.blinking = false
			}
		} else if m.rng.Intn(40) == 0 { // ~prob por tick de empezar a parpadear
			m.blinking = true
			m.blinkLeft = 2 // duración del blink
		}

		// --- glow de la boca: oscilación senoidal
		// usamos un seno discreto sin importar math, simple lookup
		phase := float64(m.tick%50) / 50.0 // 0..1
		// triangular wave: 0 -> 1 -> 0
		if phase < 0.5 {
			m.mouthGlow = phase * 2
		} else {
			m.mouthGlow = (1 - phase) * 2
		}

		// --- chispas: spawn aleatorio, decay
		// avanzar las existentes
		alive := m.sparkles[:0]
		for _, s := range m.sparkles {
			s.life--
			if s.life > 0 {
				alive = append(alive, s)
			}
		}
		m.sparkles = alive
		// spawnear nuevas
		if m.rng.Intn(2) == 0 && len(m.sparkles) < 8 {
			chars := []string{"·", "✦", "✧", "⋆", "*"}
			s := sparkle{
				row:  sparkZone.rowMin + m.rng.Intn(sparkZone.rowMax-sparkZone.rowMin+1),
				col:  sparkZone.colMin + m.rng.Intn(sparkZone.colMax-sparkZone.colMin+1),
				life: 4 + m.rng.Intn(6),
				char: chars[m.rng.Intn(len(chars))],
			}
			m.sparkles = append(m.sparkles, s)
		}

		return m, tickCmd()
	}
	return m, nil
}

// ---------------------------------------------------------------------------
// Render
// ---------------------------------------------------------------------------

// Color del cuerpo del robot (gris) — usamos el dato base RobotColors.
// Color naranja para los ojos (más saturado que el dato base, queda más vivo).
var (
	eyeColor   = lipgloss.Color("#FFB347")
	eyeBlinkColor = lipgloss.Color("#FF8C42")
	sparkColor = lipgloss.Color("#FF7A1A")
)

// interpolar entre dos colores RGB
func lerpColor(a, b [3]uint8, t float64) [3]uint8 {
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	return [3]uint8{
		uint8(float64(a[0]) + (float64(b[0])-float64(a[0]))*t),
		uint8(float64(a[1]) + (float64(b[1])-float64(a[1]))*t),
		uint8(float64(a[2]) + (float64(b[2])-float64(a[2]))*t),
	}
}

func rgbHex(c [3]uint8) string {
	return fmt.Sprintf("#%02X%02X%02X", c[0], c[1], c[2])
}

// pintar una región con frames + color
func paintRegion(grid [][]string, colors [][][3]uint8, r region, frame []string, color [3]uint8) {
	for dy, line := range frame {
		row := r.rowMin + dy
		if row < 0 || row >= len(grid) {
			continue
		}
		// recorrer runas (no bytes — son multi-byte)
		col := r.colMin
		for _, ch := range line {
			if col >= 0 && col < len(grid[row]) {
				if ch != ' ' {
					grid[row][col] = string(ch)
					colors[row][col] = color
				}
			}
			col++
		}
	}
}

func (m model) View() string {
	if m.tick == 0 && m.width == 0 {
		// primer render antes de WindowSizeMsg
		return "Cargando..."
	}

	// Copiar el grid base (chars + colors) para poder modificar.
	chars := make([][]string, RobotRows)
	colors := make([][][3]uint8, RobotRows)
	for r := 0; r < RobotRows; r++ {
		chars[r] = make([]string, RobotCols)
		colors[r] = make([][3]uint8, RobotCols)
		copy(chars[r], RobotChars[r])
		copy(colors[r], RobotColors[r])
	}

	// --- Aplicar ojos
	eyeFrame := eyeOpen
	currentEyeColor := [3]uint8{0xFF, 0xB3, 0x47}
	if m.blinking {
		eyeFrame = eyeBlink
		currentEyeColor = [3]uint8{0xFF, 0x8C, 0x42}
	} else if m.tick%200 < 30 {
		// cada cierto tiempo, "feliz" un instante
		eyeFrame = eyeHappy
	}
	paintRegion(chars, colors, leftEye, eyeFrame, currentEyeColor)
	paintRegion(chars, colors, rightEye, eyeFrame, currentEyeColor)

	// --- Aplicar glow a la boca: interpolar entre cyan oscuro y cyan brillante
	cyanDim := [3]uint8{0x10, 0x60, 0x70}
	cyanBright := [3]uint8{0x40, 0xE0, 0xFF}
	currentMouthColor := lerpColor(cyanDim, cyanBright, m.mouthGlow)
	for r := mouth.rowMin; r <= mouth.rowMax; r++ {
		for c := mouth.colMin; c <= mouth.colMax; c++ {
			if r >= 0 && r < RobotRows && c >= 0 && c < RobotCols {
				// solo recolorear celdas que YA tienen contenido (la línea de la boca)
				if chars[r][c] != " " {
					colors[r][c] = currentMouthColor
				}
			}
		}
	}

	// --- Chispas (sobrescriben celdas vacías arriba)
	for _, s := range m.sparkles {
		if s.row >= 0 && s.row < RobotRows && s.col >= 0 && s.col < RobotCols {
			// fade out: cuanto menos life, más oscuro
			t := float64(s.life) / 8.0
			if t > 1 {
				t = 1
			}
			c := lerpColor([3]uint8{0x40, 0x20, 0x00}, [3]uint8{0xFF, 0xA0, 0x40}, t)
			chars[s.row][s.col] = s.char
			colors[s.row][s.col] = c
		}
	}

	// --- Componer string final con lipgloss
	var sb strings.Builder
	for r := 0; r < RobotRows; r++ {
		for c := 0; c < RobotCols; c++ {
			ch := chars[r][c]
			if ch == " " {
				sb.WriteString(" ")
				continue
			}
			col := colors[r][c]
			style := lipgloss.NewStyle().Foreground(lipgloss.Color(rgbHex(col)))
			sb.WriteString(style.Render(ch))
		}
		sb.WriteString("\n")
	}

	// Marco con bordes redondeados
	frame := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#444")).
		Padding(1, 2).
		Render(sb.String())

	help := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#888")).
		Render("\n  q/esc para salir · braille + truecolor + lipgloss")

	return frame + help
}

// suprimir warning de eyeColor/sparkColor no usados (los dejé como referencia)
var _ = eyeColor
var _ = eyeBlinkColor
var _ = sparkColor

func main() {
	// rand seed implícito en Go 1.20+
	_ = rand.Int()
	p := tea.NewProgram(initialModel(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
