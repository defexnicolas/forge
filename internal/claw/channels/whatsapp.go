package channels

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	waCompanionReg "go.mau.fi/whatsmeow/proto/waCompanionReg"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"

	_ "modernc.org/sqlite"
)

// init sets the device descriptor whatsmeow advertises during pairing
// so WhatsApp's Linked Devices screen shows a real name instead of the
// generic "Other Device Active" placeholder. The version triple is
// arbitrary — WhatsApp doesn't validate it, but a non-zero value avoids
// looking like a malformed client.
func init() {
	store.DeviceProps.Os = proto.String("Forge")
	store.DeviceProps.PlatformType = waCompanionReg.DeviceProps_DESKTOP.Enum()
	store.DeviceProps.Version = &waCompanionReg.DeviceProps_AppVersion{
		Primary:   proto.Uint32(1),
		Secondary: proto.Uint32(0),
		Tertiary:  proto.Uint32(0),
	}
}

// WhatsAppOptions controls how the channel boots and how aggressively it
// behaves on outbound traffic. Defaults err on the conservative side
// because WhatsApp's anti-spam heuristics ban first and ask questions
// later — overshooting these limits gets the user's number flagged for
// "unusual activity" and possibly disabled. The defaults below are
// roughly half of what Meta's published business rate-limits allow.
type WhatsAppOptions struct {
	// DBPath is the SQLite store for whatsmeow's session/keys. Persisting
	// it across runs avoids re-scanning the QR every launch. Recommended
	// path: ~/.forge/claw/whatsapp.db.
	DBPath string

	// QRCallback is invoked once per session pairing with the QR data
	// the user must scan. Forge surfaces it in the Channels submenu;
	// CLI users can render it to a terminal QR.
	QRCallback func(qrData string)

	// PhoneNumber, when non-empty, switches first-time pairing from QR
	// to whatsmeow's PairPhone flow. The user types the returned code
	// into WhatsApp → Linked devices → "Link with phone number instead"
	// — no image rendering needed (the SSH-friendly path). Format:
	// international, digits only, no leading '0' (e.g. "5215551234567").
	PhoneNumber string

	// PairCodeCallback fires once with the 8-character pairing code
	// (already formatted "XXXX-XXXX") when PhoneNumber pairing is in
	// use. Ignored when QR pairing is selected.
	PairCodeCallback func(code string)

	// MaxMessagesPerMinute caps outbound throughput across all
	// recipients. 10/minute is well below Meta's "unusual" threshold.
	MaxMessagesPerMinute int

	// MinDelay / MaxDelay bracket a random sleep before each Send call.
	// Random delays inside a 2-5s window mimic a human typing reply.
	MinDelay time.Duration
	MaxDelay time.Duration

	// TypingDuration is how long the channel will keep "typing..."
	// composing presence on before the actual send. Set to 0 to
	// disable. 1.5s is a believable default.
	TypingDuration time.Duration

	// Logger optional zerolog-style logger. Nil → suppress.
	Logger waLog.Logger
}

func defaultWhatsAppOptions() WhatsAppOptions {
	return WhatsAppOptions{
		MaxMessagesPerMinute: 10,
		MinDelay:             2 * time.Second,
		MaxDelay:             5 * time.Second,
		TypingDuration:       1500 * time.Millisecond,
	}
}

// WhatsApp is the whatsmeow-backed Channel. Anti-ban behaviour:
//
//   - QR-based pairing the first time, persistent session afterwards.
//   - Per-minute rate limit (configurable, default 10/min).
//   - Random pre-send delay 2-5s.
//   - Composing presence ("typing...") for ~1.5s before sending.
//   - First-contact link guard: refuses to send a message that is just a
//     URL to a phone number forge has never messaged.
//
// None of these are bulletproof — they reduce the probability of a flag,
// they do not eliminate it. The user must still respect WhatsApp's ToS
// (no broadcasts to non-consenting recipients, no bulk solicitations).
type WhatsApp struct {
	opts   WhatsAppOptions
	logger waLog.Logger

	mu     sync.Mutex
	client *whatsmeow.Client
	store  *sqlstore.Container

	connected     bool
	lastError     string
	lastInboundAt time.Time
	notes         string
	pairedAt      time.Time

	// Inbound channel forge fans into the registry.
	inbound chan Message

	// Anti-ban bookkeeping.
	rateMu       sync.Mutex
	rateWindow   []time.Time     // timestamps of the last minute's sends
	knownContact map[string]bool // recipients we have messaged before
}

// NewWhatsApp builds an unconnected channel. The actual login happens in
// Connect() so callers can wire QRCallback first.
func NewWhatsApp(opts WhatsAppOptions) *WhatsApp {
	merged := defaultWhatsAppOptions()
	if opts.DBPath != "" {
		merged.DBPath = opts.DBPath
	}
	if opts.QRCallback != nil {
		merged.QRCallback = opts.QRCallback
	}
	merged.PhoneNumber = opts.PhoneNumber
	if opts.PairCodeCallback != nil {
		merged.PairCodeCallback = opts.PairCodeCallback
	}
	if opts.MaxMessagesPerMinute > 0 {
		merged.MaxMessagesPerMinute = opts.MaxMessagesPerMinute
	}
	if opts.MinDelay > 0 {
		merged.MinDelay = opts.MinDelay
	}
	if opts.MaxDelay > 0 {
		merged.MaxDelay = opts.MaxDelay
	}
	if opts.TypingDuration >= 0 {
		merged.TypingDuration = opts.TypingDuration
	}
	if opts.Logger != nil {
		merged.Logger = opts.Logger
	}
	return &WhatsApp{
		opts:         merged,
		logger:       merged.Logger,
		inbound:      make(chan Message, 32),
		knownContact: map[string]bool{},
	}
}

func (w *WhatsApp) Name() string     { return "whatsapp" }
func (w *WhatsApp) Provider() string { return "whatsmeow" }

func (w *WhatsApp) Status() Status {
	w.mu.Lock()
	defer w.mu.Unlock()
	// Trust whatsmeow's live websocket state over our cached `connected`
	// flag. The cached value is set on Connect/Disconnect but doesn't
	// react to silent socket drops; IsConnected reflects the real
	// link health each call.
	live := w.connected
	if w.client != nil {
		live = w.client.IsConnected() && w.client.IsLoggedIn()
	}
	notes := w.notes
	if w.client != nil && !live && w.connected {
		notes = "websocket dropped — call Connect() again"
	}
	st := Status{
		Name:             "whatsapp",
		Provider:         "whatsmeow",
		Connected:        live,
		LastError:        w.lastError,
		LastInboundAt:    w.lastInboundAt,
		SupportsRichText: false,
		Notes:            notes,
		PairedAt:         w.pairedAt,
	}
	if w.client != nil && w.client.Store != nil {
		if id := w.client.Store.ID; id != nil {
			st.AccountID = id.String()
		}
		st.AccountName = w.client.Store.PushName
	}
	return st
}

// Connect opens the SQLite store, signs in (QR scan on first run), and
// starts the inbound event pump. Idempotent: a second Connect on an
// already-connected channel is a no-op.
func (w *WhatsApp) Connect(ctx context.Context) error {
	w.mu.Lock()
	if w.connected {
		w.mu.Unlock()
		return nil
	}
	w.mu.Unlock()
	if w.opts.DBPath == "" {
		return errors.New("whatsapp: DBPath is required (e.g. ~/.forge/claw/whatsapp.db)")
	}

	logger := w.logger
	if logger == nil {
		logger = waLog.Noop
	}

	// modernc.org/sqlite registers itself as "sqlite"; whatsmeow expects
	// a database/sql driver name + DSN.
	container, err := sqlstore.New(ctx, "sqlite", "file:"+w.opts.DBPath+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)", logger)
	if err != nil {
		return fmt.Errorf("whatsapp: open store: %w", err)
	}
	device, err := container.GetFirstDevice(ctx)
	if err != nil {
		return fmt.Errorf("whatsapp: get device: %w", err)
	}
	client := whatsmeow.NewClient(device, logger)
	client.AddEventHandler(w.handleEvent)

	w.mu.Lock()
	w.store = container
	w.client = client
	w.notes = ""
	w.mu.Unlock()

	hasDevice := client.Store.ID != nil
	fmt.Fprintf(os.Stderr, "[whatsapp] Connect: dbPath=%s hasDevice=%v jid=%v\n",
		w.opts.DBPath, hasDevice, client.Store.ID)
	// New session → drive the pairing dance. Two flavours:
	//   - QR (default): the goroutine fans `code` events to QRCallback.
	//   - Phone code (PhoneNumber set): we wait for the first `code`
	//     event so whatsmeow has finished its handshake, then call
	//     PairPhone and surface the 8-char code via PairCodeCallback.
	//     The QR `code` events are ignored in this mode.
	if !hasDevice {
		qrChan, _ := client.GetQRChannel(ctx)
		if err := client.Connect(); err != nil {
			return fmt.Errorf("whatsapp: connect (qr-phase): %w", err)
		}
		usePhone := w.opts.PhoneNumber != ""
		qrReady := make(chan struct{})
		go func() {
			var notified bool
			for evt := range qrChan {
				switch evt.Event {
				case "code":
					if !notified {
						notified = true
						close(qrReady)
					}
					if usePhone {
						continue
					}
					w.mu.Lock()
					w.notes = "scan QR with WhatsApp → Linked devices"
					w.mu.Unlock()
					if w.opts.QRCallback != nil {
						w.opts.QRCallback(evt.Code)
					}
				case "success":
					w.mu.Lock()
					w.connected = true
					w.notes = "paired"
					w.pairedAt = time.Now().UTC()
					w.mu.Unlock()
				case "timeout", "err-client-outdated":
					w.mu.Lock()
					w.lastError = "pairing failed: " + evt.Event
					w.notes = w.lastError
					w.mu.Unlock()
				}
			}
		}()
		if usePhone {
			select {
			case <-qrReady:
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(20 * time.Second):
				return errors.New("whatsapp: timed out waiting for handshake before pair-phone")
			}
			// clientDisplayName must follow `Browser (OS)` and the
			// browser/OS pair has to be one the server whitelists. Plain
			// "Forge" returns 400 bad-request. "Chrome (Windows)" is the
			// safe default — WhatsApp Web's own client uses the same
			// format and the server doesn't tie it to your real OS.
			code, err := client.PairPhone(ctx, w.opts.PhoneNumber, true,
				whatsmeow.PairClientChrome, "Chrome (Windows)")
			if err != nil {
				friendly := humanizePairPhoneError(err, w.opts.PhoneNumber)
				w.mu.Lock()
				w.lastError = friendly
				w.notes = w.lastError
				w.mu.Unlock()
				return fmt.Errorf("whatsapp: pair phone: %s", friendly)
			}
			w.mu.Lock()
			w.notes = "enter pair code in WhatsApp → Linked devices"
			w.mu.Unlock()
			if w.opts.PairCodeCallback != nil {
				// whatsmeow already returns the code as "XXXX-XXXX".
				w.opts.PairCodeCallback(code)
			}
		}
		return nil
	}
	if err := client.Connect(); err != nil {
		fmt.Fprintf(os.Stderr, "[whatsapp] Connect: resume failed: %v\n", err)
		return fmt.Errorf("whatsapp: connect (resume): %w", err)
	}
	w.mu.Lock()
	w.connected = true
	w.notes = "session resumed"
	w.mu.Unlock()
	fmt.Fprintf(os.Stderr, "[whatsapp] Connect: session resumed, websocket alive (IsConnected=%v IsLoggedIn=%v)\n",
		client.IsConnected(), client.IsLoggedIn())
	// Announce ourselves as online so per-chat presence updates
	// ("typing…", "paused") actually reach the recipient. Without an
	// initial Available presence whatsmeow silently drops chat
	// presence packets, so the typing simulator never shows on the
	// contact's phone.
	if err := client.SendPresence(ctx, types.PresenceAvailable); err != nil {
		fmt.Fprintf(os.Stderr, "[whatsapp] SendPresence(Available) failed: %v\n", err)
	}
	return nil
}

// humanizePairPhoneError turns whatsmeow's PairPhone errors into a
// short message the form can show. The most common ones in the wild:
//
//   - bad-request / 400: server rejected either the number (not a real
//     WhatsApp account, missing country code) or the device descriptor.
//   - ErrPhoneNumberTooShort / ErrPhoneNumberIsNotInternational: caught
//     locally before the network call.
//
// Falls back to err.Error() when nothing matches.
func humanizePairPhoneError(err error, phone string) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, whatsmeow.ErrPhoneNumberTooShort) {
		return "Number is too short — include the country code (e.g. 1 for US/Canada)."
	}
	if errors.Is(err, whatsmeow.ErrPhoneNumberIsNotInternational) {
		return "Number starts with 0 — drop it and start with the country code."
	}
	msg := err.Error()
	if strings.Contains(msg, "400") || strings.Contains(strings.ToLower(msg), "bad-request") {
		return fmt.Sprintf("WhatsApp rejected the request (400). Check that %q is your real WhatsApp number in international format with country code (no '+', no spaces) and that WhatsApp is active on that phone.", phone)
	}
	return msg
}

func (w *WhatsApp) Disconnect() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.client != nil {
		w.client.Disconnect()
	}
	w.connected = false
	return nil
}

// Logout unlinks the device on WhatsApp's side, disconnects the local
// client, and wipes the device store so the next Connect() goes through
// a fresh QR pairing.
//
// whatsmeow.Client.Logout already handles "tell the server, then drop
// local state" but only if the server call succeeds. If it fails (e.g.
// the network is down) we still clear the client and zero the SQLite
// device record manually so the user is unblocked from re-pairing.
func (w *WhatsApp) Logout(ctx context.Context) error {
	w.mu.Lock()
	client := w.client
	store := w.store
	w.mu.Unlock()

	var serverErr error
	if client != nil {
		// Logout returns ErrNotLoggedIn if Connect was never run or the
		// session was already torn down — treat that as "nothing to do"
		// rather than a hard failure.
		if err := client.Logout(ctx); err != nil && !errors.Is(err, whatsmeow.ErrNotLoggedIn) {
			serverErr = err
		}
		client.Disconnect()
	}
	// Best-effort local cleanup. If client.Logout already deleted the
	// device this is a no-op; if it failed (offline, server error) we
	// still drop the on-disk record so re-pairing is one click away.
	if client != nil && client.Store != nil {
		_ = client.Store.Delete(ctx)
	}

	w.mu.Lock()
	w.connected = false
	w.client = nil
	w.notes = "logged out"
	w.pairedAt = time.Time{}
	w.lastError = ""
	w.mu.Unlock()
	_ = store // keep the container handle; sqlstore manages its own pool
	return serverErr
}

// Send applies the anti-ban policy then routes the message through
// whatsmeow. The order is: rate-limit check → first-contact link guard →
// random pre-send delay → composing presence → SendMessage.
//
// The To field must be a valid JID (e.g. "5215555555555@s.whatsapp.net"
// or "120363042@g.us" for groups). Forge surfaces a helper that converts
// "+52 155 555 5555" into the canonical form.
func (w *WhatsApp) Send(ctx context.Context, msg Message) (Message, error) {
	w.mu.Lock()
	client := w.client
	connected := w.connected
	w.mu.Unlock()
	if !connected || client == nil {
		return Message{}, errors.New("whatsapp: not connected")
	}
	if msg.To == "" {
		return Message{}, errors.New("whatsapp: To is required")
	}
	if msg.Body == "" {
		return Message{}, errors.New("whatsapp: empty Body")
	}

	if err := w.acquireRateSlot(); err != nil {
		return Message{}, err
	}

	if w.isFirstContactLinkOnly(msg) {
		return Message{}, errors.New("whatsapp: refused — first-contact message looks like a link-only solicitation; send a real introductory message first")
	}

	jid, err := types.ParseJID(msg.To)
	if err != nil {
		return Message{}, fmt.Errorf("whatsapp: parse JID %q: %w", msg.To, err)
	}
	// Inbound JIDs from whatsmeow can carry an agent/device suffix
	// (e.g. "119567594582255:80@lid" for the new LID identity format).
	// SendMessage rejects those with "must be a user JID with no
	// device part" — strip the agent+device so we route to the user
	// itself, not a specific paired device.
	jid = jid.ToNonAD()

	// Random pre-send delay — humans don't fire messages instantly.
	if delay := w.randomDelay(); delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return Message{}, ctx.Err()
		}
	}

	// Composing presence: WhatsApp shows "typing..." to the recipient
	// for the duration of TypingDuration before the actual message.
	// Errors used to be swallowed with `_` — log them now so a silent
	// drop (e.g. websocket re-handshaking, presence-not-available)
	// shows up in forge.log instead of looking like the simulator
	// just doesn't work.
	if w.opts.TypingDuration > 0 {
		if err := client.SendChatPresence(ctx, jid, types.ChatPresenceComposing, types.ChatPresenceMediaText); err != nil {
			fmt.Fprintf(os.Stderr, "[whatsapp] SendChatPresence(Composing) jid=%s failed: %v\n", jid.String(), err)
		}
		select {
		case <-time.After(w.opts.TypingDuration):
		case <-ctx.Done():
			_ = client.SendChatPresence(ctx, jid, types.ChatPresencePaused, types.ChatPresenceMediaText)
			return Message{}, ctx.Err()
		}
		if err := client.SendChatPresence(ctx, jid, types.ChatPresencePaused, types.ChatPresenceMediaText); err != nil {
			fmt.Fprintf(os.Stderr, "[whatsapp] SendChatPresence(Paused) jid=%s failed: %v\n", jid.String(), err)
		}
	}

	resp, err := client.SendMessage(ctx, jid, &waProto.Message{
		Conversation: proto.String(msg.Body),
	})
	if err != nil {
		w.mu.Lock()
		w.lastError = err.Error()
		w.mu.Unlock()
		return Message{}, fmt.Errorf("whatsapp: send: %w", err)
	}

	// Mark this contact as known so future link-only messages pass the
	// first-contact guard.
	w.markKnown(msg.To)

	if msg.At.IsZero() {
		msg.At = time.Now().UTC()
	}
	msg.BackendID = resp.ID
	return msg, nil
}

func (w *WhatsApp) Receive() <-chan Message { return w.inbound }

func (w *WhatsApp) handleEvent(rawEvt any) {
	// Tracer log: every whatsmeow event type passes through here. Useful
	// for diagnosing "messages aren't reaching us" — if we see other
	// event types but no Message events, the websocket is alive but the
	// inbound is being delivered as a different shape (UndecryptableMessage,
	// etc.). If we see nothing at all, the client isn't connected.
	fmt.Fprintf(os.Stderr, "[whatsapp] event %T\n", rawEvt)
	switch evt := rawEvt.(type) {
	case *events.Message:
		body := ""
		if evt.Message.GetConversation() != "" {
			body = evt.Message.GetConversation()
		} else if ext := evt.Message.GetExtendedTextMessage(); ext != nil {
			body = ext.GetText()
		}
		fmt.Fprintf(os.Stderr, "[whatsapp] event.Message id=%s from=%s chat=%s isGroup=%v bodyLen=%d\n",
			evt.Info.ID, evt.Info.Sender.String(), evt.Info.Chat.String(), evt.Info.IsGroup, len(body))
		if body == "" {
			return
		}
		w.mu.Lock()
		w.lastInboundAt = time.Now().UTC()
		client := w.client
		w.mu.Unlock()
		// Mark senders we have heard from as known so we can reply
		// without tripping the first-contact guard.
		w.markKnown(evt.Info.Sender.String())
		// Send a read receipt (blue ticks) in the background — don't
		// block whatsmeow's event dispatch on the network roundtrip.
		// Best-effort: a failed receipt just leaves the chat as
		// "delivered" instead of "read", which is fine.
		if client != nil {
			info := evt.Info
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				_ = client.MarkRead(ctx, []types.MessageID{info.ID}, time.Now(), info.Chat, info.Sender)
			}()
		}
		select {
		case w.inbound <- Message{
			To:        evt.Info.Sender.String(),
			Body:      body,
			At:        evt.Info.Timestamp,
			BackendID: evt.Info.ID,
			IsGroup:   evt.Info.IsGroup,
		}:
		default:
			// inbox full — drop. Symmetric with the registry fan-in.
		}
	case *events.Disconnected, *events.LoggedOut:
		w.mu.Lock()
		w.connected = false
		w.notes = "disconnected"
		w.mu.Unlock()
	}
}

// acquireRateSlot blocks-up-to-immediately-fails when the per-minute
// budget is exhausted. We drop old timestamps from a sliding window each
// call so the budget refills smoothly.
func (w *WhatsApp) acquireRateSlot() error {
	w.rateMu.Lock()
	defer w.rateMu.Unlock()
	now := time.Now()
	cutoff := now.Add(-time.Minute)
	kept := w.rateWindow[:0]
	for _, t := range w.rateWindow {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	w.rateWindow = kept
	if len(w.rateWindow) >= w.opts.MaxMessagesPerMinute {
		return fmt.Errorf("whatsapp: rate limit (%d/min) reached; waiting protects the account", w.opts.MaxMessagesPerMinute)
	}
	w.rateWindow = append(w.rateWindow, now)
	return nil
}

func (w *WhatsApp) randomDelay() time.Duration {
	if w.opts.MaxDelay <= w.opts.MinDelay {
		return w.opts.MinDelay
	}
	span := w.opts.MaxDelay - w.opts.MinDelay
	return w.opts.MinDelay + time.Duration(rand.Int63n(int64(span)))
}

// isFirstContactLinkOnly is the cheapest spammy-pattern check we can do
// without false-positiving real conversations: if we have never messaged
// this recipient AND the message is essentially just a URL, refuse.
func (w *WhatsApp) isFirstContactLinkOnly(msg Message) bool {
	w.rateMu.Lock()
	known := w.knownContact[msg.To]
	w.rateMu.Unlock()
	if known {
		return false
	}
	body := msg.Body
	if !looksLikeURLOnly(body) {
		return false
	}
	return true
}

func looksLikeURLOnly(body string) bool {
	body = trimSpaceASCII(body)
	if body == "" {
		return false
	}
	if !(hasPrefixASCII(body, "http://") || hasPrefixASCII(body, "https://") || hasPrefixASCII(body, "wa.me/")) {
		return false
	}
	for _, r := range body {
		if r == ' ' || r == '\t' || r == '\n' {
			return false
		}
	}
	return true
}

func (w *WhatsApp) markKnown(to string) {
	w.rateMu.Lock()
	w.knownContact[to] = true
	w.rateMu.Unlock()
}

// Tiny ASCII helpers so we can avoid importing strings just for two ops
// in the hot path.

func trimSpaceASCII(s string) string {
	start := 0
	for start < len(s) && (s[start] == ' ' || s[start] == '\t' || s[start] == '\n' || s[start] == '\r') {
		start++
	}
	end := len(s)
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\n' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}

func hasPrefixASCII(s, prefix string) bool {
	if len(s) < len(prefix) {
		return false
	}
	for i := 0; i < len(prefix); i++ {
		if s[i] != prefix[i] {
			return false
		}
	}
	return true
}
