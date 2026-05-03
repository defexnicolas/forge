package tui

import (
	"fmt"
	"strings"
)

// clawSection identifies which submenu of the Claw HUB view is currently
// rendered. The previous single-block view dumped every Claw fact at once
// and was hard to scan; switching to per-section panels gives the user a
// clear navigation tree (Status / Chat / Identity / Soul / Memory /
// Heartbeat / Channels) and a place to land future settings forms.
type clawSection int

const (
	clawSectionStatus clawSection = iota
	clawSectionChat
	clawSectionIdentity
	clawSectionSoul
	clawSectionMemory
	clawSectionHeartbeat
	clawSectionChannels
)

// clawSectionTabs is the strict ordering used by the tab strip and the
// left/right cycle. Order matches the user's mental model: see status
// first, jump to chat second, then drill into specific facets.
var clawSectionTabs = []struct {
	id    clawSection
	label string
}{
	{clawSectionStatus, "Status"},
	{clawSectionChat, "Chat"},
	{clawSectionIdentity, "Identity"},
	{clawSectionSoul, "Soul"},
	{clawSectionMemory, "Memory"},
	{clawSectionHeartbeat, "Heartbeat"},
	{clawSectionChannels, "Channels"},
}

func (m *shellModel) cycleClawSection(delta int) {
	n := len(clawSectionTabs)
	if n == 0 {
		return
	}
	current := 0
	for i, t := range clawSectionTabs {
		if t.id == m.clawSection {
			current = i
			break
		}
	}
	current = (current + delta + n) % n
	m.clawSection = clawSectionTabs[current].id
}

// renderClawTabs paints the section header strip with the active section
// highlighted. Width-aware truncation is intentionally skipped — the tab
// labels are short enough to fit any reasonable terminal width.
func (m shellModel) renderClawTabs() string {
	t := m.theme
	parts := make([]string, 0, len(clawSectionTabs))
	for _, tab := range clawSectionTabs {
		label := tab.label
		if tab.id == m.clawSection {
			parts = append(parts, t.StatusValue.Render("["+label+"]"))
		} else {
			parts = append(parts, t.Muted.Render(" "+label+" "))
		}
	}
	return strings.Join(parts, " ") + "\n" + t.Muted.Render("←/→ switch section")
}

// renderClawStatus is the at-a-glance overview that used to be the entire
// Claw view. Now lives behind the Status tab.
func (m shellModel) renderClawStatus() string {
	service := m.hubClawService()
	if service == nil {
		return m.theme.Muted.Render("Claw service unavailable.")
	}
	status := service.Status()
	var b strings.Builder
	toolsLabel := "off (chitchat-only, no API spend)"
	if cfg, ok := m.loadHubSettingsConfig(); ok && cfg.Claw.ToolsEnabled {
		toolsLabel = "ON (Claw can call web_search / web_fetch / whatsapp_send)"
	}
	fmt.Fprintf(&b, "Heartbeat: %s\n", status.State.Heartbeat.Status)
	fmt.Fprintf(&b, "Identity: %s | tone=%s | style=%s\n", status.State.Identity.Name, status.State.Identity.Tone, status.State.Identity.Style)
	fmt.Fprintf(&b, "User: %s | timezone=%s\n", valueOr(status.State.User.DisplayName, "User"), valueOr(status.State.User.Timezone, "-"))
	fmt.Fprintf(&b, "Forge model: %s / %s (%d/%d ctx)\n", status.ActiveModel.ProviderName, status.ActiveModel.ModelID, status.ActiveModel.LoadedContextLength, status.ActiveModel.MaxContextLength)
	fmt.Fprintf(&b, "Tools: %s\n", toolsLabel)
	fmt.Fprintf(&b, "Memory: %d events, %d summaries, %d suggestions\n", len(status.State.Memory.Events), len(status.State.Memory.Summaries), len(status.State.Memory.Suggestions))
	fmt.Fprintf(&b, "Interview: %s\n", m.clawInterviewLabel(status.State.Interview))
	fmt.Fprintf(&b, "Chat session: %s | turns=%d\n", valueOr(status.State.Chat.SessionID, "-"), len(status.State.Chat.Transcript))
	b.WriteString("\n")
	b.WriteString(m.theme.Muted.Render("Tab actions: S start/stop heartbeat | D run dream | R reset chat"))
	b.WriteString("\n")
	b.WriteString(m.theme.Muted.Render("CLI: /claw status | /claw reset | /claw memory | /claw soul | /claw cron add ..."))
	return strings.TrimRight(b.String(), "\n")
}

func (m shellModel) renderClawIdentity() string {
	service := m.hubClawService()
	if service == nil {
		return m.theme.Muted.Render("Claw service unavailable.")
	}
	id := service.Status().State.Identity
	t := m.theme
	var b strings.Builder
	b.WriteString(t.Accent.Render("Identity") + "\n\n")
	fmt.Fprintf(&b, "Name: %s\n", valueOr(id.Name, "(unset)"))
	fmt.Fprintf(&b, "Tone: %s\n", valueOr(id.Tone, "(unset)"))
	fmt.Fprintf(&b, "Style: %s\n", valueOr(id.Style, "(unset)"))
	fmt.Fprintf(&b, "Seed: %s\n", valueOr(id.Seed, "(unset)"))
	b.WriteString("\n" + t.Muted.Render("Press Enter to edit name / tone / style / seed."))
	return b.String()
}

func (m shellModel) renderClawSoul() string {
	service := m.hubClawService()
	if service == nil {
		return m.theme.Muted.Render("Claw service unavailable.")
	}
	soul := service.Status().State.Soul
	t := m.theme
	var b strings.Builder
	b.WriteString(t.Accent.Render("Soul") + "\n\n")
	if len(soul.Values) > 0 {
		b.WriteString("Values:\n")
		for _, v := range soul.Values {
			b.WriteString("  • " + v + "\n")
		}
		b.WriteString("\n")
	}
	if len(soul.Goals) > 0 {
		b.WriteString("Goals:\n")
		for _, g := range soul.Goals {
			b.WriteString("  • " + g + "\n")
		}
		b.WriteString("\n")
	}
	if len(soul.Traits) > 0 {
		b.WriteString("Traits: " + strings.Join(soul.Traits, ", ") + "\n\n")
	}
	if len(soul.LearnedNotes) > 0 {
		b.WriteString("Learned notes (latest first):\n")
		notes := soul.LearnedNotes
		for i := len(notes) - 1; i >= 0 && i > len(notes)-6; i-- {
			b.WriteString("  • " + previewShellText(notes[i], 140) + "\n")
		}
	}
	if b.Len() == len(t.Accent.Render("Soul"))+2 {
		b.WriteString(t.Muted.Render("(empty — finish the interview to populate Claw's soul)"))
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m shellModel) renderClawMemory() string {
	service := m.hubClawService()
	if service == nil {
		return m.theme.Muted.Render("Claw service unavailable.")
	}
	mem := service.Status().State.Memory
	t := m.theme
	var b strings.Builder
	b.WriteString(t.Accent.Render("Memory") + "\n\n")
	fmt.Fprintf(&b, "Events: %d  Summaries: %d  Suggestions: %d\n\n", len(mem.Events), len(mem.Summaries), len(mem.Suggestions))
	if len(mem.Summaries) > 0 {
		b.WriteString(t.Accent.Render("Latest summaries") + "\n")
		summaries := mem.Summaries
		for i := len(summaries) - 1; i >= 0 && i > len(summaries)-4; i-- {
			b.WriteString("  • " + previewShellText(summaries[i].Summary, 140) + "\n")
		}
		b.WriteString("\n")
	}
	if len(mem.Suggestions) > 0 {
		b.WriteString(t.Accent.Render("Active suggestions") + "\n")
		sugg := mem.Suggestions
		for i := len(sugg) - 1; i >= 0 && i > len(sugg)-4; i-- {
			b.WriteString("  • " + previewShellText(sugg[i].Summary, 140) + "\n")
		}
		b.WriteString("\n")
	}
	if len(mem.Events) > 0 {
		b.WriteString(t.Accent.Render("Recent events") + "\n")
		events := mem.Events
		for i := len(events) - 1; i >= 0 && i > len(events)-6; i-- {
			ev := events[i]
			fmt.Fprintf(&b, "  [%s] %s: %s\n", ev.Kind, valueOr(ev.Author, "—"), previewShellText(ev.Text, 100))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m shellModel) renderClawHeartbeat() string {
	service := m.hubClawService()
	if service == nil {
		return m.theme.Muted.Render("Claw service unavailable.")
	}
	hb := service.Status().State.Heartbeat
	t := m.theme
	var b strings.Builder
	b.WriteString(t.Accent.Render("Heartbeat") + "\n\n")
	fmt.Fprintf(&b, "State: %s\n", hb.Status)
	fmt.Fprintf(&b, "Running: %t\n", hb.Running)
	if !hb.LastBeatAt.IsZero() {
		fmt.Fprintf(&b, "Last beat: %s\n", hb.LastBeatAt.Format("2006-01-02 15:04:05"))
	}
	if !hb.LastStartedAt.IsZero() {
		fmt.Fprintf(&b, "Last started: %s\n", hb.LastStartedAt.Format("2006-01-02 15:04:05"))
	}
	if hb.LastError != "" {
		fmt.Fprintf(&b, "Last error: %s\n", hb.LastError)
	}
	b.WriteString("\n" + t.Muted.Render("S: start/stop heartbeat  D: run dream now"))
	b.WriteString("\n" + t.Muted.Render("Enter: edit cadences (heartbeat seconds + dream minutes)."))
	return b.String()
}

// knownChannelProvider lists every transport forge can talk to, even if
// the user has not configured one yet. Surfaces them in the Channels
// submenu as placeholders so the user knows "this slot exists, it's just
// not connected" without having to read the docs.
type knownChannelProvider struct {
	Name        string
	Provider    string
	StatusHint  string
	Description string
}

var knownChannelProviders = []knownChannelProvider{
	{
		Name:        "mock",
		Provider:    "inbox",
		StatusHint:  "always available",
		Description: "in-memory channel for /claw inbox; no external delivery",
	},
	{
		Name:        "whatsapp",
		Provider:    "whatsmeow",
		StatusHint:  "(not configured)",
		Description: "WhatsApp Web via QR-code login; rate-limit + typing-simulation gated",
	},
}

// allowlistModeLabel returns "permissive" or "strict" for footer hints.
func allowlistModeLabel(enabled bool) string {
	if enabled {
		return "strict"
	}
	return "permissive"
}

func (m shellModel) selectedClawChannel() knownChannelProvider {
	if len(knownChannelProviders) == 0 {
		return knownChannelProvider{}
	}
	idx := m.clawChannelIndex
	if idx < 0 {
		idx = 0
	}
	if idx >= len(knownChannelProviders) {
		idx = len(knownChannelProviders) - 1
	}
	return knownChannelProviders[idx]
}

func (m shellModel) renderClawChannels() string {
	service := m.hubClawService()
	if service == nil {
		return m.theme.Muted.Render("Claw service unavailable.")
	}
	ch := service.Status().State.Channels
	t := m.theme
	var b strings.Builder
	b.WriteString(t.Accent.Render("Channels") + "\n\n")
	fmt.Fprintf(&b, "Default: %s\n\n", valueOr(ch.Default, "(none)"))

	// Iterate the static catalog so the user sees every supported
	// transport. Live state from service.Status() overrides the
	// placeholder when the user has actually configured the channel.
	for i, known := range knownChannelProviders {
		live, hasLive := ch.Items[known.Name]
		state := known.StatusHint
		if hasLive {
			if live.Enabled {
				state = "enabled"
			} else {
				state = "registered, disabled"
			}
		}
		cursor := " "
		if m.clawChannelSelect && i == m.clawChannelIndex {
			cursor = ">"
		}
		fmt.Fprintf(&b, "%s %-10s  provider=%-10s  %s\n", cursor, known.Name, known.Provider, state)
		b.WriteString("    " + t.Muted.Render(known.Description) + "\n")
		if hasLive {
			if live.AccountID != "" {
				fmt.Fprintf(&b, "    account: %s\n", live.AccountID)
			}
			if live.AccountName != "" {
				fmt.Fprintf(&b, "    name:    %s\n", live.AccountName)
			}
			if !live.PairedAt.IsZero() {
				fmt.Fprintf(&b, "    paired:  %s\n", live.PairedAt.Local().Format("2006-01-02 15:04:05"))
			}
			if !live.LastInboundAt.IsZero() {
				fmt.Fprintf(&b, "    last inbound: %s\n", live.LastInboundAt.Local().Format("2006-01-02 15:04:05"))
			}
			if live.LastError != "" {
				fmt.Fprintf(&b, "    last error: %s\n", live.LastError)
			}
			mode := "permissive (everyone gets a reply)"
			if live.AllowlistEnabled {
				mode = t.Warning.Render("strict") + " (only allowlist replies)"
			}
			fmt.Fprintf(&b, "    allowlist: %s — %d entries\n", mode, len(live.Allowlist))
			if live.AllowlistEnabled && len(live.Allowlist) > 0 {
				maxShow := 5
				for j, jid := range live.Allowlist {
					if j >= maxShow {
						fmt.Fprintf(&b, "      ... and %d more\n", len(live.Allowlist)-maxShow)
						break
					}
					fmt.Fprintf(&b, "      • %s\n", jid)
				}
			}
		}
		b.WriteString("\n")
	}
	selected := m.selectedClawChannel()
	selectedLive, selectedHasLive := ch.Items[selected.Name]
	if !m.clawChannelSelect {
		b.WriteString(t.Muted.Render("Enter: open channel selector"))
		b.WriteString("\n")
		b.WriteString(t.Muted.Render("Once open: ↑/↓ move, Enter confirm, Esc close"))
		return strings.TrimRight(b.String(), "\n")
	}
	b.WriteString(t.Muted.Render("↑/↓ select channel"))
	b.WriteString("\n")
	switch selected.Name {
	case "whatsapp":
		if selectedHasLive && selectedLive.Enabled {
			if m.clawLogoutPending {
				b.WriteString(t.Warning.Render("Press L again to confirm logout (this unlinks WhatsApp from this device)."))
			} else {
				b.WriteString(t.Muted.Render("Enter: re-pair   L: logout   T: toggle allowlist (" + allowlistModeLabel(selectedLive.AllowlistEnabled) + ")   A: add JID   R: remove JID"))
			}
		} else {
			b.WriteString(t.Muted.Render("Enter: open WhatsApp pairing form (QR scan + anti-ban guardrails)."))
		}
	case "mock":
		b.WriteString(t.Muted.Render("Enter: show mock channel usage."))
	default:
		b.WriteString(t.Muted.Render("Enter: open selected channel."))
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderClawChat is the conversational pane: shows recent transcript and
// the input box (or "Press Enter to chat" hint when not focused). Keeps
// the existing chat plumbing — only the framing moves into a tab.
func (m shellModel) renderClawChat() string {
	service := m.hubClawService()
	if service == nil {
		return m.theme.Muted.Render("Claw service unavailable.")
	}
	status := service.Status()
	t := m.theme
	var b strings.Builder
	if !status.State.Interview.Active && status.State.Interview.CompletedAt.IsZero() {
		b.WriteString(t.Muted.Render("Run /claw interview from the CLI first to personalise Claw before chatting."))
		return b.String()
	}
	sectionTitle := "Chat"
	if status.State.Interview.Active {
		sectionTitle = "Interview"
	}
	b.WriteString(t.Accent.Render(sectionTitle) + "\n\n")
	turns := status.State.Chat.Transcript
	if status.State.Interview.Active {
		turns = status.State.Interview.Transcript
	}
	if len(turns) > 12 {
		turns = turns[len(turns)-12:]
	}
	for _, turn := range turns {
		prefix := "You"
		if turn.Speaker == "claw" {
			prefix = status.State.Identity.Name
		}
		b.WriteString(prefix + ": " + compactShellText(turn.Text) + "\n")
	}
	showPending, showThinking := clawPendingTranscriptState(turns, m.clawPendingAnswer)
	if showPending {
		b.WriteString("You: " + compactShellText(m.clawPendingAnswer) + "\n")
	}
	if showThinking {
		b.WriteString(status.State.Identity.Name + ": " + t.Muted.Render("thinking...") + "\n")
	}
	if m.clawInputEnabled() {
		b.WriteString("\n")
		if m.activePane == paneInput {
			if m.clawAwaitingReply {
				b.WriteString(t.Muted.Render("Waiting for Claw to respond..."))
			} else {
				b.WriteString(m.clawInputBoxView())
			}
		} else {
			if status.State.Interview.Active {
				b.WriteString(t.Muted.Render("Press Enter to answer the current question."))
			} else {
				b.WriteString(t.Muted.Render("Press Enter to chat with Claw."))
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// sortStrings is a tiny shim so we don't pull in sort just for one use.
// Bubble sort is fine for the tiny channel-name slice (typically 1-3
// entries).
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
