package llm

import "testing"

func TestBuildChatPayloadOmitsSamplingWhenUnset(t *testing.T) {
	payload := buildChatPayload(ChatRequest{
		Model:    "local-model",
		Messages: []Message{{Role: "user", Content: "hello"}},
	}, false)

	if got := payload["model"]; got != "local-model" {
		t.Fatalf("model = %#v, want local-model", got)
	}
	if got := payload["stream"]; got != false {
		t.Fatalf("stream = %#v, want false", got)
	}
	if _, ok := payload["temperature"]; ok {
		t.Fatalf("temperature should be omitted when unset: %#v", payload)
	}
}

func TestBuildChatPayloadIncludesTemperatureWhenExplicit(t *testing.T) {
	temp := 0.6
	payload := buildChatPayload(ChatRequest{
		Model:       "local-model",
		Messages:    []Message{{Role: "user", Content: "hello"}},
		Temperature: &temp,
	}, true)

	got, ok := payload["temperature"]
	if !ok {
		t.Fatalf("temperature missing from payload: %#v", payload)
	}
	if got != temp {
		t.Fatalf("temperature = %#v, want %v", got, temp)
	}
	if payload["stream"] != true {
		t.Fatalf("stream = %#v, want true", payload["stream"])
	}
}
