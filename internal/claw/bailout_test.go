package claw

import "testing"

func TestLooksLikeToolBailout(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"empty", "", false},
		{"plain answer", "Sure, here's the result of the search.", false},
		{"english refusal", "I can't send WhatsApp messages from this environment.", true},
		{"english cannot", "I cannot do that here, sorry.", true},
		{"spanish refusal", "Lo siento, no puedo enviar mensajes en este entorno.", true},
		{"spanish capacity", "No tengo la capacidad de enviar WhatsApp.", true},
		{"as an ai", "As an AI assistant, I'm just an AI and cannot perform that.", true},
		{"reply mentions phrase mid-paragraph", "Aquí va una respuesta larga que responde la pregunta con detalles, ejemplos y referencias. La fuente dice que no puedo prometer una garantía absoluta pero la mayoría coincide. Lorem ipsum lorem ipsum.", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := looksLikeToolBailout(tc.in); got != tc.want {
				t.Fatalf("looksLikeToolBailout(%q) = %v; want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestBailoutNudgePhoneSend(t *testing.T) {
	got := bailoutNudge("envíale un mensaje por whatsapp a +573214447235, dile hola desde Forge")
	if got == "" {
		t.Fatal("expected nudge for phone+send pattern, got empty")
	}
	if !contains(got, "whatsapp_send") {
		t.Fatalf("expected nudge to name whatsapp_send, got %q", got)
	}
}

func TestBailoutNudgeReminder(t *testing.T) {
	got := bailoutNudge("recuérdame en 30 segundos que revise el horno")
	if got == "" {
		t.Fatal("expected nudge for reminder pattern, got empty")
	}
	if !contains(got, "claw_schedule_reminder") {
		t.Fatalf("expected nudge to name claw_schedule_reminder, got %q", got)
	}
}

func TestBailoutNudgeCron(t *testing.T) {
	got := bailoutNudge("cada día a las 9 mándame un resumen")
	if got == "" {
		t.Fatal("expected nudge for cron pattern, got empty")
	}
	if !contains(got, "claw_add_cron") {
		t.Fatalf("expected nudge to name claw_add_cron, got %q", got)
	}
}

func TestBailoutNudgeUnknown(t *testing.T) {
	if got := bailoutNudge("hola, cómo estás?"); got != "" {
		t.Fatalf("expected empty nudge for chitchat, got %q", got)
	}
}

func TestContainsPhoneNumber(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"+573214447235", true},
		{"call me at +1 (555) 123-4567 today", true},
		{"+12", false},
		{"plain text", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := containsPhoneNumber(tc.in); got != tc.want {
			t.Errorf("containsPhoneNumber(%q) = %v want %v", tc.in, got, tc.want)
		}
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
