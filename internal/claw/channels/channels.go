// Package channels defines the pluggable transport interface Claw uses
// to send and receive messages. Each implementation (mock, whatsapp, in
// the future telegram/slack/etc.) plugs into the same Channel interface
// so the rest of Claw stays transport-agnostic.
//
// Lifecycle:
//
//	registry := channels.NewRegistry()
//	registry.Register(channels.NewMock())
//	registry.Register(whatsapp.New(opts))   // when wired
//
//	for name := range registry.List() {
//	    if ch, ok := registry.Get(name); ok && !ch.Status().Connected {
//	        _ = ch.Connect(ctx)
//	    }
//	}
//
//	_ = registry.Send(ctx, "whatsapp", channels.Message{
//	    To:   "5215555555555@s.whatsapp.net",
//	    Body: "Reminder: 6pm dentist",
//	})
//
// The registry is concurrency-safe; each channel implementation owns its
// own goroutine for inbound delivery (Receive returns a channel that
// fans out to subscribers).
package channels

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Message is the wire-level shape every backend speaks. Body is the only
// required field; everything else is optional metadata that callers may
// or may not have available.
type Message struct {
	// To is the recipient address (channel-specific format — phone JID
	// for whatsapp, channel ID for slack, "user" for mock).
	To string
	// Body is the textual payload. Plain text by default; channels that
	// support markdown can advertise it via Status().SupportsRichText.
	Body string
	// At marks when Claw produced this message. Defaults to now if zero.
	At time.Time
	// IDs (optional) propagate any backend-assigned message ID after Send
	// returns. Used by future delivery-receipt + edit/delete features.
	BackendID string
	// Source is the name of the channel that produced this message,
	// stamped automatically by Registry's fan-in goroutine on inbound
	// traffic. Outbound messages leave this empty — callers know which
	// channel they are sending to. Inbound consumers use it to route
	// the message into per-channel state (memory events, status
	// counters, etc.) without having to thread the channel pointer.
	Source string
	// IsGroup signals that the inbound message came from a group chat.
	// Set by the WhatsApp backend (true when the chat JID server is
	// "g.us") so consumers can skip auto-replies on groups — bots
	// chiming in on every group message is intrusive at best and a
	// quick way to get banned at worst. Outbound messages leave this
	// false; senders pick the recipient JID directly.
	IsGroup bool
}

// Status reports whether a channel is alive and any operator-visible
// metadata. Returned by Channel.Status() and surfaced in the Hub UI.
type Status struct {
	Name             string
	Provider         string
	Connected        bool
	LastError        string
	LastInboundAt    time.Time
	SupportsRichText bool
	// Notes lets a backend explain its current state ("waiting for QR
	// scan", "rate-limited until 21:00", etc.) without inventing a new
	// status enum for each scenario.
	Notes string

	// Identity-of-the-paired-account fields. Optional — backends without
	// the concept of a per-link account (e.g. webhook posters) leave
	// these empty.
	//
	//   AccountID  is the canonical identifier the backend uses to
	//              address the account itself. For WhatsApp this is the
	//              phone-number JID; for Telegram it would be the bot
	//              token's user ID, etc.
	//   AccountName is the human-friendly handle the backend exposes.
	//              For WhatsApp this is the device's PushName.
	//   PairedAt    timestamp of the last successful pairing.
	AccountID   string
	AccountName string
	PairedAt    time.Time
}

// Channel is the verb-set every backend implements.
//
// Implementations should be safe to call from multiple goroutines and
// must respect ctx cancellation in Connect/Send. Receive returns a
// long-lived chan that the registry fan-out goroutine reads.
type Channel interface {
	Name() string
	Provider() string
	Status() Status
	Connect(ctx context.Context) error
	Disconnect() error
	Send(ctx context.Context, msg Message) (Message, error)
	// Receive returns the inbound message stream. Implementations may
	// return the same channel across calls or a new one — registry only
	// reads it. nil means "no inbound support" (e.g. a write-only
	// integration like a webhook poster).
	Receive() <-chan Message
}

// Registry tracks every Channel by name and offers a one-call Send by
// channel name plus a fan-in goroutine that consolidates inbound traffic
// from every connected channel into a single inbound stream.
type Registry struct {
	mu       sync.RWMutex
	channels map[string]Channel

	inbound chan Message
	cancels map[string]context.CancelFunc
}

func NewRegistry() *Registry {
	return &Registry{
		channels: map[string]Channel{},
		inbound:  make(chan Message, 32),
		cancels:  map[string]context.CancelFunc{},
	}
}

// Register adds a channel. If a channel with the same name already
// exists, Register replaces it and disconnects the old one.
func (r *Registry) Register(ch Channel) {
	if ch == nil {
		return
	}
	r.mu.Lock()
	if existing, ok := r.channels[ch.Name()]; ok {
		_ = existing.Disconnect()
		if cancel := r.cancels[ch.Name()]; cancel != nil {
			cancel()
		}
	}
	r.channels[ch.Name()] = ch
	// Spin a fan-in goroutine for this channel's inbound stream. The
	// goroutine stamps msg.Source with the channel's name before
	// fanning in, so downstream consumers can identify the origin
	// without a per-channel registration table.
	if recv := ch.Receive(); recv != nil {
		ctx, cancel := context.WithCancel(context.Background())
		r.cancels[ch.Name()] = cancel
		go r.fanIn(ctx, recv, ch.Name())
	}
	r.mu.Unlock()
}

func (r *Registry) fanIn(ctx context.Context, recv <-chan Message, source string) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-recv:
			if !ok {
				return
			}
			if msg.Source == "" {
				msg.Source = source
			}
			select {
			case r.inbound <- msg:
			case <-ctx.Done():
				return
			default:
				// Drop on full inbox rather than block a backend's
				// receive loop. Claw's inbox is for triage, not
				// guaranteed delivery — drops should be rare with the
				// 32-slot buffer.
			}
		}
	}
}

func (r *Registry) Get(name string) (Channel, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ch, ok := r.channels[name]
	return ch, ok
}

func (r *Registry) List() []Channel {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Channel, 0, len(r.channels))
	for _, ch := range r.channels {
		out = append(out, ch)
	}
	return out
}

func (r *Registry) Send(ctx context.Context, channelName string, msg Message) (Message, error) {
	r.mu.RLock()
	ch, ok := r.channels[channelName]
	r.mu.RUnlock()
	if !ok {
		return Message{}, fmt.Errorf("channel not registered: %s", channelName)
	}
	return ch.Send(ctx, msg)
}

// Inbound returns the consolidated inbound stream every registered
// channel feeds into. Subscribers should treat it as best-effort
// (messages may be dropped under load — see fanIn comment).
func (r *Registry) Inbound() <-chan Message {
	return r.inbound
}

// Shutdown disconnects every registered channel and stops fan-in
// goroutines. Safe to call multiple times; subsequent calls are no-ops.
func (r *Registry) Shutdown() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, cancel := range r.cancels {
		cancel()
	}
	r.cancels = map[string]context.CancelFunc{}
	for _, ch := range r.channels {
		_ = ch.Disconnect()
	}
}
