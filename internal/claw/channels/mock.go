package channels

import (
	"context"
	"sync"
	"time"
)

// Mock is the in-memory channel forge ships with by default. Useful for
// tests, demos, and anything driven from inside forge itself (the CLI
// `/claw inbox <text>` command lands here). Sent messages are appended
// to .Outbound so a test can assert what was emitted; Inject() lets a
// test pretend a remote sender wrote in.
type Mock struct {
	mu       sync.Mutex
	name     string
	outbound []Message
	inbound  chan Message
	connected bool
}

func NewMock() *Mock {
	return &Mock{
		name:    "mock",
		inbound: make(chan Message, 16),
	}
}

func (m *Mock) Name() string     { return m.name }
func (m *Mock) Provider() string { return "inbox" }

func (m *Mock) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return Status{
		Name:             m.name,
		Provider:         "inbox",
		Connected:        m.connected,
		SupportsRichText: false,
		Notes:            "in-memory; no external delivery",
	}
}

func (m *Mock) Connect(_ context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.connected = true
	return nil
}

func (m *Mock) Disconnect() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.connected = false
	return nil
}

func (m *Mock) Send(_ context.Context, msg Message) (Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if msg.At.IsZero() {
		msg.At = time.Now().UTC()
	}
	m.outbound = append(m.outbound, msg)
	return msg, nil
}

func (m *Mock) Receive() <-chan Message { return m.inbound }

// Outbound returns a copy of the messages this mock has accepted via
// Send. Test-only helper; the live UI should not depend on it.
func (m *Mock) Outbound() []Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Message, len(m.outbound))
	copy(out, m.outbound)
	return out
}

// Inject pretends a remote sender wrote in. Drops on full buffer rather
// than blocking the test goroutine.
func (m *Mock) Inject(msg Message) {
	if msg.At.IsZero() {
		msg.At = time.Now().UTC()
	}
	select {
	case m.inbound <- msg:
	default:
	}
}
