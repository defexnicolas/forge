package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"forge/internal/claw/channels"
	"forge/internal/globalconfig"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/skip2/go-qrcode"
)

// whatsAppForm walks the user through pairing a WhatsApp account with
// the Claw companion. The flow is:
//
//	idle  -> user presses Enter to start
//	wait  -> forge spins up the channel; whatsmeow asks for a QR scan
//	qr    -> forge displays the QR code text the user pastes / scans
//	done  -> session paired; status flips to Connected and the form closes
type whatsAppForm struct {
	state    *whatsAppFormState // pointer so the form value remains copy-safe
	done     bool
	canceled bool
	pulse    int
	theme    Theme
}

// whatsAppFormState holds the mutex-protected fields the QR/pairing
// goroutine writes to. Keeping it behind a pointer means whatsAppForm
// itself stays trivially copyable (bubbletea passes models by value).
type whatsAppFormState struct {
	mu            sync.Mutex
	phase         waFormPhase
	qrText        string // raw token, kept as fallback when PNG generation fails
	qrPNGPath     string // absolute path of the on-disk QR image
	qrPNGErr      string // last error from PNG write or open ("" if none)
	pngOpenedOnce bool   // viewer is launched once per pairing; refreshes overwrite the file silently
	statusMsg     string
	errMsg        string
	channel       *channels.WhatsApp
	cancel        context.CancelFunc

	// Pairing-mode picker + phone-code state
	mode       waPairMode
	modeCursor int    // 0 = QR, 1 = Phone code
	phoneInput string // digits only, no leading '+'; max 15 (E.164)
	pairCode   string // formatted "XXXX-XXXX" once whatsmeow returns it
}

type waSnapshot struct {
	phase      waFormPhase
	qrText     string
	pngPath    string
	pngErr     string
	statusMsg  string
	errMsg     string
	mode       waPairMode
	modeCursor int
	phoneInput string
	pairCode   string
}

func (s *whatsAppFormState) snapshot() waSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return waSnapshot{
		phase:      s.phase,
		qrText:     s.qrText,
		pngPath:    s.qrPNGPath,
		pngErr:     s.qrPNGErr,
		statusMsg:  s.statusMsg,
		errMsg:     s.errMsg,
		mode:       s.mode,
		modeCursor: s.modeCursor,
		phoneInput: s.phoneInput,
		pairCode:   s.pairCode,
	}
}

type waFormPhase int

const (
	waPhaseIdle waFormPhase = iota
	waPhaseModeSelect
	waPhasePhoneInput
	waPhaseConnecting
	waPhaseQR
	waPhasePairCode
	waPhaseConnected
	waPhaseError
)

type waPairMode int

const (
	waModeQR waPairMode = iota
	waModePhone
)

// waPairedMsg fires once the device is paired (whatsmeow's "success"
// event). waErrorMsg surfaces a connect failure. waRefreshMsg is the
// poll tick that drives spinner animations and re-snapshots state.
type waPairedMsg struct{}
type waErrorMsg struct{ Err string }
type waRefreshMsg struct{}

const waRefreshInterval = 200 * time.Millisecond

func tickWhatsAppForm() tea.Cmd {
	return tea.Tick(waRefreshInterval, func(time.Time) tea.Msg {
		return waRefreshMsg{}
	})
}

func newWhatsAppForm(theme Theme) whatsAppForm {
	return whatsAppForm{state: &whatsAppFormState{phase: waPhaseIdle}, theme: theme}
}

// whatsAppQRPath returns the on-disk location where the QR PNG is
// written for each pairing attempt. The path is fixed (overwritten on
// each new token) so we don't accumulate stale images across runs.
func whatsAppQRPath() string {
	return filepath.Join(globalconfig.HomeDir(), "cache", "qr", "whatsapp.png")
}

// writeQRPNG materializes the pairing token as a 512px PNG at
// whatsAppQRPath() and, on the first invocation per pairing, asks the
// OS to open it in the default image viewer. Returns the absolute path
// (always set, even on error so the user can locate the file manually)
// and a non-nil error if the PNG could not be written or opened.
//
// whatsmeow rotates the QR every ~20s; we deliberately suppress
// re-launching the viewer on later refreshes to avoid stealing focus
// repeatedly. Most image viewers reload from disk when the underlying
// file changes.
func writeQRPNG(token string, openOnce bool) (string, error) {
	path := whatsAppQRPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return path, err
	}
	if err := qrcode.WriteFile(token, qrcode.Medium, 512, path); err != nil {
		return path, err
	}
	if openOnce {
		if err := openInDefaultApp(path); err != nil {
			return path, err
		}
	}
	return path, nil
}

func (f whatsAppForm) Update(msg tea.Msg) (whatsAppForm, tea.Cmd) {
	if f.state == nil {
		f.state = &whatsAppFormState{}
	}
	switch m := msg.(type) {
	case waPairedMsg:
		f.state.mu.Lock()
		f.state.phase = waPhaseConnected
		f.state.statusMsg = "Paired. Claw can now send and receive on this number."
		f.state.mu.Unlock()
		return f, nil
	case waErrorMsg:
		f.state.mu.Lock()
		f.state.phase = waPhaseError
		f.state.errMsg = m.Err
		f.state.mu.Unlock()
		return f, nil
	case waRefreshMsg:
		f.pulse++
		snap := f.state.snapshot()
		if snap.phase == waPhaseConnecting || snap.phase == waPhaseQR || snap.phase == waPhasePairCode {
			return f, tickWhatsAppForm()
		}
		return f, nil
	case tea.KeyMsg:
		snap := f.state.snapshot()

		// Phone-input phase wants per-rune handling and inline validation.
		if snap.phase == waPhasePhoneInput {
			return f.updatePhoneInput(m)
		}

		// Mode-select consumes 1/2 and arrows before generic Enter/Esc.
		if snap.phase == waPhaseModeSelect {
			return f.updateModeSelect(m)
		}

		switch m.Type {
		case tea.KeyEsc:
			f.state.mu.Lock()
			cancel := f.state.cancel
			f.state.mu.Unlock()
			if cancel != nil {
				cancel()
			}
			f.canceled = true
			f.done = true
			return f, nil
		case tea.KeyEnter:
			if snap.phase == waPhaseIdle || snap.phase == waPhaseError {
				f.state.mu.Lock()
				f.state.phase = waPhaseModeSelect
				f.state.errMsg = ""
				f.state.pngOpenedOnce = false
				f.state.qrPNGErr = ""
				f.state.mu.Unlock()
				return f, nil
			}
			if snap.phase == waPhaseQR && snap.pngPath != "" {
				// Re-open the PNG. Best-effort: surface the error in
				// state so the form can show it next frame.
				if err := openInDefaultApp(snap.pngPath); err != nil {
					f.state.mu.Lock()
					f.state.qrPNGErr = err.Error()
					f.state.mu.Unlock()
				}
				return f, nil
			}
			if snap.phase == waPhaseConnected {
				f.done = true
				return f, nil
			}
		}
	}
	return f, nil
}

// updateModeSelect handles key input when the user is choosing between
// QR pairing and phone-code pairing. Digit shortcuts (1/2) jump directly
// to the option; Tab/Up/Down toggle; Enter confirms; Esc closes the form.
func (f whatsAppForm) updateModeSelect(m tea.KeyMsg) (whatsAppForm, tea.Cmd) {
	switch m.Type {
	case tea.KeyEsc:
		f.canceled = true
		f.done = true
		return f, nil
	case tea.KeyUp, tea.KeyDown, tea.KeyTab:
		f.state.mu.Lock()
		if f.state.modeCursor == 0 {
			f.state.modeCursor = 1
		} else {
			f.state.modeCursor = 0
		}
		f.state.mu.Unlock()
		return f, nil
	case tea.KeyRunes:
		for _, r := range m.Runes {
			switch r {
			case '1':
				f.state.mu.Lock()
				f.state.modeCursor = 0
				f.state.mu.Unlock()
			case '2':
				f.state.mu.Lock()
				f.state.modeCursor = 1
				f.state.mu.Unlock()
			}
		}
		return f, nil
	case tea.KeyEnter:
		f.state.mu.Lock()
		if f.state.modeCursor == 0 {
			f.state.mode = waModeQR
			f.state.phase = waPhaseConnecting
			f.state.statusMsg = "Connecting to WhatsApp servers..."
			f.state.errMsg = ""
			f.state.pngOpenedOnce = false
			f.state.qrPNGErr = ""
			f.state.mu.Unlock()
			return f, tea.Batch(f.startPairing(), tickWhatsAppForm())
		}
		f.state.mode = waModePhone
		f.state.phase = waPhasePhoneInput
		f.state.errMsg = ""
		f.state.mu.Unlock()
		return f, nil
	}
	return f, nil
}

// updatePhoneInput handles digit-by-digit entry of the international
// phone number. Only digits are accepted; Backspace trims; Enter
// validates (>=7 digits, not starting with '0') and kicks off pairing;
// Esc clears the buffer and goes back to mode-select.
func (f whatsAppForm) updatePhoneInput(m tea.KeyMsg) (whatsAppForm, tea.Cmd) {
	switch m.Type {
	case tea.KeyEsc:
		f.state.mu.Lock()
		f.state.phase = waPhaseModeSelect
		f.state.phoneInput = ""
		f.state.errMsg = ""
		f.state.mu.Unlock()
		return f, nil
	case tea.KeyBackspace, tea.KeyDelete:
		f.state.mu.Lock()
		if n := len(f.state.phoneInput); n > 0 {
			f.state.phoneInput = f.state.phoneInput[:n-1]
		}
		f.state.errMsg = ""
		f.state.mu.Unlock()
		return f, nil
	case tea.KeySpace:
		return f, nil
	case tea.KeyEnter:
		f.state.mu.Lock()
		phone := f.state.phoneInput
		f.state.mu.Unlock()
		if len(phone) < 7 {
			f.state.mu.Lock()
			f.state.errMsg = "Number too short — use international format, digits only."
			f.state.mu.Unlock()
			return f, nil
		}
		if phone[0] == '0' {
			f.state.mu.Lock()
			f.state.errMsg = "Drop the leading 0 — start with the country code (e.g. 52 for Mexico)."
			f.state.mu.Unlock()
			return f, nil
		}
		f.state.mu.Lock()
		f.state.phase = waPhaseConnecting
		f.state.statusMsg = "Asking WhatsApp for a pairing code..."
		f.state.errMsg = ""
		f.state.mu.Unlock()
		return f, tea.Batch(f.startPairing(), tickWhatsAppForm())
	case tea.KeyRunes:
		f.state.mu.Lock()
		added := false
		for _, r := range m.Runes {
			if unicode.IsDigit(r) && len(f.state.phoneInput) < 15 {
				f.state.phoneInput += string(r)
				added = true
			}
		}
		if added {
			f.state.errMsg = ""
		}
		f.state.mu.Unlock()
		return f, nil
	}
	return f, nil
}

// startPairing kicks off the WhatsApp channel boot in a goroutine and
// streams the QR / paired / error transitions back into the form's
// shared state via the mutex. The form's renderer reads the latest
// snapshot each frame.
func (f whatsAppForm) startPairing() tea.Cmd {
	state := f.state
	return func() tea.Msg {
		dbDir := filepath.Join(globalconfig.HomeDir(), "claw")
		dbPath := filepath.Join(dbDir, "whatsapp.db")
		ctx, cancel := context.WithCancel(context.Background())
		state.mu.Lock()
		state.cancel = cancel
		state.mu.Unlock()

		state.mu.Lock()
		mode := state.mode
		phone := state.phoneInput
		state.mu.Unlock()
		var phoneOpt string
		if mode == waModePhone {
			phoneOpt = phone
		}
		ch := channels.NewWhatsApp(channels.WhatsAppOptions{
			DBPath:      dbPath,
			PhoneNumber: phoneOpt,
			QRCallback: func(qr string) {
				// In phone-code mode the channel layer also drops these,
				// but a TUI-side guard avoids any flicker if a stale
				// event leaks through.
				state.mu.Lock()
				if state.mode == waModePhone {
					state.mu.Unlock()
					return
				}
				// First-pass state update with the raw token so the
				// form can show *something* immediately even if PNG
				// generation hiccups.
				state.qrText = qr
				state.phase = waPhaseQR
				openOnce := !state.pngOpenedOnce
				state.mu.Unlock()

				path, err := writeQRPNG(qr, openOnce)

				state.mu.Lock()
				state.qrPNGPath = path
				if err != nil {
					state.qrPNGErr = err.Error()
					state.statusMsg = "Couldn't open the QR image automatically — open it manually:"
				} else {
					state.qrPNGErr = ""
					state.statusMsg = "Your QR is open in the system image viewer. Scan it with WhatsApp → Linked devices."
					if openOnce {
						state.pngOpenedOnce = true
					}
				}
				state.mu.Unlock()
			},
			PairCodeCallback: func(code string) {
				state.mu.Lock()
				state.pairCode = code
				state.phase = waPhasePairCode
				state.statusMsg = "Enter this code in WhatsApp → Linked devices."
				state.errMsg = ""
				state.mu.Unlock()
			},
		})
		if err := ch.Connect(ctx); err != nil {
			return waErrorMsg{Err: err.Error()}
		}
		state.mu.Lock()
		state.channel = ch
		state.mu.Unlock()
		go func() {
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					st := ch.Status()
					if st.Connected {
						state.mu.Lock()
						state.phase = waPhaseConnected
						state.statusMsg = "Paired. Claw can now send and receive on this number."
						state.mu.Unlock()
						return
					}
				}
			}
		}()
		return nil
	}
}

func (f whatsAppForm) View() string {
	return f.ViewSized(56, 0)
}

// renderQRAscii returns the QR encoded as half-block characters (each
// terminal row covers two QR pixels via U+2580). The result is square
// and scan-able from any monospaced terminal, including SSH sessions
// where the on-disk PNG is unreachable. Returns "" when the token is
// empty or the encoder rejects it.
func renderQRAscii(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	q, err := qrcode.New(token, qrcode.Medium)
	if err != nil {
		return ""
	}
	bitmap := q.Bitmap()
	if len(bitmap) == 0 {
		return ""
	}
	const (
		full  = "██" // ██  both pixels black
		upper = "▀▀" // ▀▀  top black, bottom white
		lower = "▄▄" // ▄▄  top white, bottom black
		blank = "  "           // both white
	)
	var b strings.Builder
	rows := len(bitmap)
	for y := 0; y < rows; y += 2 {
		for x := range bitmap[y] {
			top := bitmap[y][x]
			bot := false
			if y+1 < rows {
				bot = bitmap[y+1][x]
			}
			switch {
			case top && bot:
				b.WriteString(full)
			case top && !bot:
				b.WriteString(upper)
			case !top && bot:
				b.WriteString(lower)
			default:
				b.WriteString(blank)
			}
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

func (f whatsAppForm) ViewSized(maxWidth, maxHeight int) string {
	t := f.theme
	if maxWidth <= 0 {
		maxWidth = 56
	}

	snap := f.state.snapshot()
	// QR phase needs more horizontal room — go-qrcode at Medium produces
	// a 33-module matrix at minimum, which is 66 columns when each module
	// is rendered as two terminal cells. Bump the box width to keep the
	// QR un-wrapped; the rest of the form stays compact.
	if snap.phase == waPhaseQR && maxWidth < 80 {
		maxWidth = 80
	}

	var b strings.Builder
	title := t.TableHeader.Render("WhatsApp pairing")

	switch snap.phase {
	case waPhaseIdle:
		b.WriteString("This will open a WhatsApp Web session bound to forge.\n")
		b.WriteString("Guardrails: typing delay, random send delay, rate limit, first-contact link block.\n")
		b.WriteString("\n" + t.Muted.Render("Enter: start pairing  Esc: cancel"))
	case waPhaseModeSelect:
		b.WriteString("Choose how to pair this device:\n\n")
		opt0 := "  [1] QR scan (best with desktop / image viewer)"
		opt1 := "  [2] Phone code (recommended over SSH)"
		if snap.modeCursor == 0 {
			opt0 = "> " + t.StatusValue.Render("[1] QR scan (best with desktop / image viewer)")
		} else {
			opt1 = "> " + t.StatusValue.Render("[2] Phone code (recommended over SSH)")
		}
		b.WriteString(opt0 + "\n")
		b.WriteString(opt1 + "\n")
		b.WriteString("\n" + t.Muted.Render("1/2 or ↑/↓ to choose · Enter: confirm · Esc: cancel"))
	case waPhasePhoneInput:
		b.WriteString("Phone number (international format, digits only, no leading 0):\n\n")
		display := "+" + snap.phoneInput
		if snap.phoneInput == "" {
			display = "+_________________"
		}
		b.WriteString("  " + t.StatusValue.Render(display) + "\n\n")
		b.WriteString(t.Muted.Render("e.g. +5215551234567 (52 = country code, then the rest)") + "\n")
		if snap.errMsg != "" {
			b.WriteString("\n" + t.ErrorStyle.Render(snap.errMsg) + "\n")
		}
		b.WriteString("\n" + t.Muted.Render("Enter: request pair code · Backspace: edit · Esc: back"))
	case waPhaseConnecting:
		spinner := []string{"-", "\\", "|", "/"}[f.pulse%4]
		b.WriteString("\n" + t.StatusValue.Render(spinner+" "+snap.statusMsg) + "\n\n")
		b.WriteString(t.Muted.Render("Booting the WhatsApp client and opening the local session store.") + "\n\n")
		b.WriteString(t.Muted.Render("Esc: abort"))
	case waPhaseQR:
		// SSH-friendly: render the QR as text directly in the form so
		// users without a desktop (or who would rather not context-switch
		// to an image viewer) can scan straight from the terminal. The
		// PNG on disk is still written for users who prefer it.
		b.WriteString("\n" + t.StatusValue.Render(snap.statusMsg) + "\n\n")
		if ascii := renderQRAscii(snap.qrText); ascii != "" {
			b.WriteString(ascii + "\n\n")
		}
		if snap.pngErr != "" {
			b.WriteString(t.ErrorStyle.Render(snap.pngErr) + "\n\n")
		}
		if snap.pngPath != "" {
			b.WriteString(t.Muted.Render("Also saved as PNG: ") + snap.pngPath + "\n")
		}
		if snap.pngErr != "" && snap.qrText != "" {
			// Last-resort fallback: dump the raw token so the user can
			// paste it into a web-based QR generator and scan from there.
			b.WriteString("\n" + t.Muted.Render("Raw token (paste into a web QR generator if needed):") + "\n")
			b.WriteString(snap.qrText + "\n")
		}
		b.WriteString("\n" + t.Muted.Render("Enter: reopen viewer   Esc: cancel"))
	case waPhasePairCode:
		spinner := []string{"-", "\\", "|", "/"}[f.pulse%4]
		b.WriteString("\n" + t.StatusValue.Render(spinner+" Pair code: "+snap.pairCode) + "\n\n")
		b.WriteString("On your phone:\n")
		b.WriteString("  WhatsApp → Settings → Linked devices →\n")
		b.WriteString("  \"Link with phone number instead\" → enter this code\n\n")
		b.WriteString(t.Muted.Render("Code expires in ~160s; this screen advances automatically once linked.") + "\n")
		b.WriteString("\n" + t.Muted.Render("Esc: cancel"))
	case waPhaseConnected:
		b.WriteString("\n" + t.Success.Render(snap.statusMsg) + "\n\n")
		b.WriteString(t.Muted.Render("Enter: close"))
	case waPhaseError:
		b.WriteString("\n" + t.ErrorStyle.Render("Pairing failed: "+snap.errMsg) + "\n\n")
		b.WriteString(t.Muted.Render("Enter: retry  Esc: cancel"))
	}

	body := strings.TrimSpace(b.String())
	content := body
	if title != "" {
		content = title + "\n" + body
	}
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#5f87d7")).
		Padding(0, 1).
		Width(maxWidth).
		Align(lipgloss.Center)
	return box.Render(content)
}

// Channel returns the underlying *channels.WhatsApp once pairing
// completes. Returns nil before Connect() succeeds. Used by
// shell_forms.go to hand the live channel to the Claw service so the
// whatsapp_send tool can route through it.
func (f whatsAppForm) Channel() *channels.WhatsApp {
	if f.state == nil {
		return nil
	}
	f.state.mu.Lock()
	defer f.state.mu.Unlock()
	return f.state.channel
}
