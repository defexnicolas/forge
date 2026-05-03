package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestWhatsAppSendCallsRegisteredSender(t *testing.T) {
	var capturedTo, capturedBody string
	reg := NewRegistry()
	RegisterWhatsAppSendTool(reg, func(_ context.Context, to, body string) error {
		capturedTo = to
		capturedBody = body
		return nil
	})
	tool, ok := reg.Get("whatsapp_send")
	if !ok {
		t.Fatal("whatsapp_send not registered")
	}
	in, _ := json.Marshal(map[string]any{
		"to":   "5215555555555@s.whatsapp.net",
		"body": "ola",
	})
	res, err := tool.Run(Context{Context: context.Background()}, in)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if capturedTo != "5215555555555@s.whatsapp.net" || capturedBody != "ola" {
		t.Errorf("sender received to=%q body=%q", capturedTo, capturedBody)
	}
	if !strings.Contains(res.Summary, "delivered") {
		t.Errorf("summary should confirm delivery, got %q", res.Summary)
	}
}

func TestWhatsAppSendSurfacesSenderErrorAsResult(t *testing.T) {
	reg := NewRegistry()
	RegisterWhatsAppSendTool(reg, func(_ context.Context, _, _ string) error {
		return errors.New("rate limit reached")
	})
	tool, _ := reg.Get("whatsapp_send")
	in, _ := json.Marshal(map[string]any{"to": "x@s.whatsapp.net", "body": "y"})
	// Sender errors should NOT be returned as Go errors — the model must
	// see them as tool-result text so it can decide to wait/retry.
	res, err := tool.Run(Context{Context: context.Background()}, in)
	if err != nil {
		t.Fatalf("expected sender error to be embedded in result, got Go error: %v", err)
	}
	if !strings.Contains(res.Summary, "rate limit reached") {
		t.Errorf("expected sender error in summary, got %q", res.Summary)
	}
}

func TestWhatsAppSendValidatesRequiredFields(t *testing.T) {
	reg := NewRegistry()
	RegisterWhatsAppSendTool(reg, func(context.Context, string, string) error { return nil })
	tool, _ := reg.Get("whatsapp_send")
	cases := []struct {
		name string
		in   string
	}{
		{"missing to", `{"body":"hi"}`},
		{"missing body", `{"to":"x@s.whatsapp.net"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := tool.Run(Context{Context: context.Background()}, json.RawMessage(c.in))
			if err == nil {
				t.Error("expected validation error")
			}
		})
	}
}

func TestWhatsAppSendErrorsWhenSenderUnregistered(t *testing.T) {
	reg := NewRegistry()
	RegisterWhatsAppSendTool(reg, nil) // no sender wired
	tool, _ := reg.Get("whatsapp_send")
	in, _ := json.Marshal(map[string]any{"to": "x@s.whatsapp.net", "body": "y"})
	_, err := tool.Run(Context{Context: context.Background()}, in)
	if err == nil {
		t.Fatal("expected error when sender closure is nil")
	}
}

func TestWhatsAppSendPermissionAsk(t *testing.T) {
	tool := whatsAppSendTool{}
	got := tool.Permission(Context{}, nil)
	if got.Decision != PermissionAsk {
		t.Errorf("Permission = %v, want PermissionAsk (auto profile is the only escape)", got.Decision)
	}
}
