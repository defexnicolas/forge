package channels

import (
	"strings"
	"testing"
	"time"
)

// TestWhatsAppRateLimitTrips verifies the per-minute budget actually
// rejects sends past the cap. Doesn't talk to a real server — only
// exercises the acquireRateSlot policy gate.
func TestWhatsAppRateLimitTrips(t *testing.T) {
	w := NewWhatsApp(WhatsAppOptions{
		MaxMessagesPerMinute: 3,
	})
	for i := 0; i < 3; i++ {
		if err := w.acquireRateSlot(); err != nil {
			t.Fatalf("send %d should fit in budget: %v", i+1, err)
		}
	}
	if err := w.acquireRateSlot(); err == nil {
		t.Fatal("expected rate-limit error on 4th send within the minute")
	}
}

// TestWhatsAppRateLimitRefillsAfterMinute backdates the window so the
// first three timestamps fall outside the minute, freeing slots without
// us having to actually wait 60s in the test.
func TestWhatsAppRateLimitRefillsAfterMinute(t *testing.T) {
	w := NewWhatsApp(WhatsAppOptions{MaxMessagesPerMinute: 2})
	w.rateMu.Lock()
	w.rateWindow = []time.Time{
		time.Now().Add(-2 * time.Minute),
		time.Now().Add(-90 * time.Second),
	}
	w.rateMu.Unlock()
	if err := w.acquireRateSlot(); err != nil {
		t.Errorf("expected stale entries to be evicted, got %v", err)
	}
}

// TestWhatsAppFirstContactLinkOnlyBlocked covers the spam-pattern
// shortcut: a brand-new recipient + a body that is just a URL = refused.
func TestWhatsAppFirstContactLinkOnlyBlocked(t *testing.T) {
	w := NewWhatsApp(WhatsAppOptions{})
	msg := Message{To: "5215555555555@s.whatsapp.net", Body: "https://example.com/promo"}
	if !w.isFirstContactLinkOnly(msg) {
		t.Error("expected first-contact link-only to be flagged")
	}
}

func TestWhatsAppFirstContactLinkAllowedAfterPriorMessage(t *testing.T) {
	w := NewWhatsApp(WhatsAppOptions{})
	w.markKnown("5215555555555@s.whatsapp.net")
	msg := Message{To: "5215555555555@s.whatsapp.net", Body: "https://example.com/promo"}
	if w.isFirstContactLinkOnly(msg) {
		t.Error("known contact should be exempt from link-only guard")
	}
}

func TestWhatsAppFirstContactRealMessageAllowed(t *testing.T) {
	w := NewWhatsApp(WhatsAppOptions{})
	msg := Message{To: "fresh@s.whatsapp.net", Body: "Hola, soy Nico, te escribo desde Forge."}
	if w.isFirstContactLinkOnly(msg) {
		t.Error("real introductory text must not trip the link-only guard")
	}
}

// TestWhatsAppRandomDelayWithinBracket ensures the jitter stays inside
// [MinDelay, MaxDelay] — drift here would translate to either too-quick
// sends (ban risk) or too-slow ones (user thinks Claw is broken).
func TestWhatsAppRandomDelayWithinBracket(t *testing.T) {
	w := NewWhatsApp(WhatsAppOptions{
		MinDelay: 100 * time.Millisecond,
		MaxDelay: 200 * time.Millisecond,
	})
	for i := 0; i < 200; i++ {
		d := w.randomDelay()
		if d < 100*time.Millisecond || d >= 200*time.Millisecond {
			t.Fatalf("delay %v outside [100ms,200ms)", d)
		}
	}
}

func TestLooksLikeURLOnlyHeuristic(t *testing.T) {
	cases := []struct {
		body string
		want bool
	}{
		{"https://example.com", true},
		{"http://example.com/path", true},
		{"wa.me/521555", true},
		{"  https://example.com\n", true},
		{"hello https://example.com", false}, // has whitespace before URL
		{"https://example.com hello", false}, // text after URL
		{"plain text", false},
		{"", false},
	}
	for _, c := range cases {
		if got := looksLikeURLOnly(c.body); got != c.want {
			t.Errorf("looksLikeURLOnly(%q) = %t, want %t", c.body, got, c.want)
		}
	}
}

// TestWhatsAppStatusReportsConnectionState round-trips a Status snapshot
// after we manually flip the Connected flag (Connect/Disconnect would
// require a live whatsmeow client).
func TestWhatsAppStatusReportsConnectionState(t *testing.T) {
	w := NewWhatsApp(WhatsAppOptions{})
	st := w.Status()
	if st.Connected {
		t.Error("fresh WhatsApp should report Connected=false")
	}
	if st.Provider != "whatsmeow" {
		t.Errorf("Provider = %q", st.Provider)
	}
	if !strings.EqualFold(st.Name, "whatsapp") {
		t.Errorf("Name = %q", st.Name)
	}
}
