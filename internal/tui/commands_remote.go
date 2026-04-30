package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"forge/internal/remote"

	tea "github.com/charmbracelet/bubbletea"
)

// remoteControlHandle owns the HTTP server lifetime plus the goroutine that
// drains remote inputs into the TUI as tea messages.
type remoteControlHandle struct {
	server *remote.Server
	cancel context.CancelFunc
}

// remoteInputMsg is delivered when a connected web client submits a prompt or
// slash command. The TUI handles it in the main update loop so it lands on the
// same queue as keyboard input.
type remoteInputMsg struct {
	Kind string
	Text string
}

func (m *model) handleRemoteCommand(fields []string) string {
	t := m.theme
	sub := ""
	if len(fields) >= 2 {
		sub = fields[1]
	}
	switch sub {
	case "start":
		return m.startRemote(fields)
	case "stop":
		return m.stopRemote()
	case "status":
		return m.remoteStatus()
	case "":
		return m.remoteOverview()
	default:
		return t.Warning.Render("Unknown subcommand: "+sub) + "\n" + m.remoteOverview()
	}
}

func (m *model) remoteOverview() string {
	t := m.theme
	var b strings.Builder
	b.WriteString(t.Accent.Render("Remote control") + "\n")
	if m.remoteServer != nil {
		srv := m.remoteServer.server
		fmt.Fprintf(&b, "  %s\n", t.Success.Render("running"))
		fmt.Fprintf(&b, "  URL:     %s\n", srv.LANURL())
		fmt.Fprintf(&b, "  Addr:    %s\n", srv.Addr())
		fmt.Fprintf(&b, "  Token:   %s\n", srv.Token())
		fmt.Fprintf(&b, "  Viewers: %d\n", srv.SubscriberCount())
	} else {
		fmt.Fprintf(&b, "  %s\n", t.Muted.Render("off"))
	}
	b.WriteString("\n")
	b.WriteString(t.Muted.Render("Subcommands:") + "\n")
	b.WriteString("  /remote-control start [port]   start the LAN server (default port 9595)\n")
	b.WriteString("  /remote-control stop           stop the server\n")
	b.WriteString("  /remote-control status         show URL, token, and viewer count")
	return b.String()
}

func (m *model) startRemote(fields []string) string {
	t := m.theme
	if m.remoteServer != nil {
		return t.Warning.Render("Remote server already running at " + m.remoteServer.server.LANURL())
	}
	port := 9595
	if len(fields) >= 3 {
		if p, err := strconv.Atoi(fields[2]); err == nil && p > 0 && p < 65536 {
			port = p
		}
	}
	hub := remote.NewHub()
	srv, err := remote.New(remote.Config{
		Port:      port,
		Hub:       hub,
		SessionFn: func() any { return m.remoteSessionSnapshot() },
	})
	if err != nil {
		return t.ErrorStyle.Render("remote: " + err.Error())
	}
	if err := srv.Start(); err != nil {
		return t.ErrorStyle.Render("remote start: " + err.Error())
	}
	m.agentRuntime.EventTee = hub

	ctx, cancel := context.WithCancel(context.Background())
	m.remoteServer = &remoteControlHandle{server: srv, cancel: cancel}

	// Drain incoming remote inputs into the TUI via tea.Program messages.
	// We append a pendingCommand so Bubble Tea invokes the next-step cmd.
	m.pendingCommand = pumpRemoteInputs(ctx, srv.Inputs())

	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", t.Success.Render("Remote control running."))
	fmt.Fprintf(&b, "  URL:   %s\n", srv.LANURL())
	fmt.Fprintf(&b, "  Addr:  %s\n", srv.Addr())
	fmt.Fprintf(&b, "  Token: %s\n", srv.Token())
	b.WriteString(t.Muted.Render("  Open the URL on another device on the same network."))
	return b.String()
}

func (m *model) stopRemote() string {
	t := m.theme
	if m.remoteServer == nil {
		return t.Muted.Render("Remote server is not running.")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = m.remoteServer.server.Stop(ctx)
	m.remoteServer.cancel()
	m.remoteServer = nil
	m.agentRuntime.EventTee = nil
	return t.Success.Render("Remote control stopped.")
}

func (m *model) remoteStatus() string {
	t := m.theme
	if m.remoteServer == nil {
		return t.Muted.Render("Remote control: off. Use /remote-control start to enable.")
	}
	srv := m.remoteServer.server
	return fmt.Sprintf("%s\n  URL: %s\n  Viewers: %d",
		t.Success.Render("Remote control: on"),
		srv.LANURL(),
		srv.SubscriberCount())
}

// pumpRemoteInputs returns a tea.Cmd that reads one input from the remote
// server and dispatches it as a remoteInputMsg; the Update loop re-schedules
// it for each subsequent input.
func pumpRemoteInputs(ctx context.Context, inputs <-chan remote.Input) tea.Cmd {
	return func() tea.Msg {
		select {
		case <-ctx.Done():
			return nil
		case in, ok := <-inputs:
			if !ok {
				return nil
			}
			return remoteInputMsg{Kind: in.Kind, Text: in.Text}
		}
	}
}

func (m model) remoteSessionSnapshot() map[string]any {
	return map[string]any{
		"cwd":        m.options.CWD,
		"mode":       m.agentRuntime.Mode,
		"model":      currentModelName(m),
		"status":     stripAnsi(m.statusLineView()),
		"history":    m.remoteHistoryLines(),
		"streaming":  m.streaming,
		"activeRole": m.activeModelRole(),
	}
}

func (m model) remoteHistoryLines() []string {
	lines := append([]string(nil), m.history...)
	if m.streaming && m.streamingStartIdx >= 0 {
		streamingLine := indentBlock(formatStreamingText(m.streamingRaw.String(), m.thinkEnabled, m.theme), "    ")
		if m.streamingStartIdx < len(lines) {
			lines[m.streamingStartIdx] = streamingLine
		} else {
			lines = append(lines, streamingLine)
		}
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, stripAnsi(line))
	}
	return out
}
