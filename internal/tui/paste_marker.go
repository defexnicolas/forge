package tui

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// pastedBlock holds one captured paste. Text is the raw bytes the user
// pasted (preserved verbatim including newlines and any leading
// indentation); Lines is a precomputed line count used purely to
// render the inline marker.
type pastedBlock struct {
	ID    int
	Lines int
	Text  string
}

// pasteMinLines is the threshold below which a paste flows into the
// textarea unchanged. Two-line pastes are usually small snippets the
// user wants to see and edit; three lines and up are the case where
// the textarea explodes vertically and the marker is the right call.
const pasteMinLines = 3

// pasteMarkerRE matches the inline marker we insert into the textarea
// when collapsing a paste. Matches both "[Pasted text #N +M lines]"
// and "[Pasted text #N +M line]" (for the 1-line case if we ever
// lower pasteMinLines to 1).
var pasteMarkerRE = regexp.MustCompile(`\[Pasted text #(\d+) \+\d+ lines?\]`)

// interceptPasteKey rewrites a KeyMsg containing a multi-line paste
// (msg.Paste true OR runes containing newlines) into a marker key the
// textarea can absorb without blowing up vertically. The original
// content is stashed in m.pastes under the new id and is expanded
// back at submit time by expandPastes.
//
// Returns the (possibly rewritten) message. Single-line pastes and
// non-paste KeyMsgs pass through unchanged so editing snippets
// remains direct.
func (m *model) interceptPasteKey(msg tea.Msg) tea.Msg {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok || keyMsg.Type != tea.KeyRunes || len(keyMsg.Runes) == 0 {
		return msg
	}
	content := string(keyMsg.Runes)
	// Bubbletea sets msg.Paste when the terminal reports bracketed
	// paste; we also accept a multi-rune burst that happens to
	// include a newline as evidence of a paste — some terminals
	// don't send paste markers but a typed Enter is its own KeyEnter
	// event, so a literal '\n' inside a KeyRunes payload is virtually
	// always a paste.
	isPaste := keyMsg.Paste || strings.Contains(content, "\n")
	if !isPaste {
		return msg
	}
	lines := strings.Count(content, "\n") + 1
	if lines < pasteMinLines {
		return msg
	}

	if m.pastes == nil {
		m.pastes = map[int]pastedBlock{}
	}
	m.pasteCounter++
	id := m.pasteCounter
	m.pastes[id] = pastedBlock{ID: id, Lines: lines, Text: content}

	marker := fmt.Sprintf("[Pasted text #%d +%d lines]", id, lines)
	keyMsg.Runes = []rune(marker)
	keyMsg.Paste = false
	return keyMsg
}

// expandPastes replaces every "[Pasted text #N +M lines]" marker in
// `line` with the raw paste body that was captured under id N. Markers
// without a matching entry (e.g. typed by the user verbatim) are left
// alone — that lets the user reference a previous paste's id by hand
// across turns and still get the body inlined when they submit.
func (m *model) expandPastes(line string) string {
	if !strings.Contains(line, "[Pasted text #") {
		return line
	}
	return pasteMarkerRE.ReplaceAllStringFunc(line, func(match string) string {
		sub := pasteMarkerRE.FindStringSubmatch(match)
		if len(sub) < 2 {
			return match
		}
		id, err := strconv.Atoi(sub[1])
		if err != nil {
			return match
		}
		if block, ok := m.pastes[id]; ok {
			return block.Text
		}
		return match
	})
}
