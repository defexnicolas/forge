package channels

import (
	"context"
	"testing"
	"time"
)

func TestRegistrySendRoutesToNamedChannel(t *testing.T) {
	r := NewRegistry()
	mock := NewMock()
	r.Register(mock)

	if _, err := r.Send(context.Background(), "mock", Message{To: "user", Body: "hello"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	out := mock.Outbound()
	if len(out) != 1 || out[0].Body != "hello" {
		t.Errorf("expected one outbound 'hello', got %#v", out)
	}
}

func TestRegistrySendOnUnknownChannelErrors(t *testing.T) {
	r := NewRegistry()
	if _, err := r.Send(context.Background(), "missing", Message{Body: "x"}); err == nil {
		t.Fatal("expected error for unregistered channel")
	}
}

func TestRegistryFanInDeliversInboundFromMock(t *testing.T) {
	r := NewRegistry()
	mock := NewMock()
	r.Register(mock)

	mock.Inject(Message{Body: "from outside"})

	select {
	case got := <-r.Inbound():
		if got.Body != "from outside" {
			t.Errorf("inbound body = %q, want 'from outside'", got.Body)
		}
		if got.Source != "mock" {
			t.Errorf("Source = %q, want 'mock' (fan-in must stamp origin)", got.Source)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for fanned-in message")
	}
}

func TestRegistryShutdownDisconnectsChannels(t *testing.T) {
	r := NewRegistry()
	mock := NewMock()
	r.Register(mock)
	if err := mock.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !mock.Status().Connected {
		t.Fatal("expected Connected=true after Connect")
	}
	r.Shutdown()
	if mock.Status().Connected {
		t.Error("expected Connected=false after registry shutdown")
	}
}

func TestRegisterReplacesExistingChannelByName(t *testing.T) {
	r := NewRegistry()
	first := NewMock()
	second := NewMock()
	r.Register(first)
	r.Register(second)
	got, ok := r.Get("mock")
	if !ok {
		t.Fatal("mock channel missing after re-register")
	}
	if got != second {
		t.Errorf("expected second mock to win, got %p (first=%p second=%p)", got, first, second)
	}
}
