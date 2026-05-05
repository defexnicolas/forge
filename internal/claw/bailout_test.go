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

func TestLooksLikePartialBailout(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"empty", "", false},
		{"plain answer", "Listo, ya envié tu mensaje.", false},
		{"the urgp regression — reminder ok, whatsapp invented excuse",
			"El recordatorio está configurado: te avisaré a la 1:44 a. m. (hora de Bogotá) para que te vayas a dormir. Sin embargo, el mensaje por WhatsApp no se pudo enviar porque el canal no está registrado en este entorno. Tendrás que enviarlo manualmente.",
			true},
		{"english variant", "I scheduled the reminder. However, the WhatsApp message could not be sent because the channel is not registered. You'll have to send it manually.", true},
		{"pivot but no give-up", "Saved the contact. However, you might also want to add a note.", false},
		{"give-up but no pivot (single failure not partial)", "No se pudo enviar el mensaje.", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := looksLikePartialBailout(tc.in); got != tc.want {
				t.Fatalf("looksLikePartialBailout = %v; want %v", got, tc.want)
			}
		})
	}
}

func TestPartialBailoutNudgeNamesWhatsApp(t *testing.T) {
	got := partialBailoutNudge("1:44A.M me envias un mensaje por whatsapp a este numero +573214447235", map[string]bool{"claw_schedule_reminder": true})
	if got == "" {
		t.Fatal("expected nudge when whatsapp_send was skipped despite phone+send request")
	}
	if !contains(got, "whatsapp_send") {
		t.Fatalf("expected nudge to name whatsapp_send, got %q", got)
	}
}

func TestPartialBailoutNudgeSilentWhenToolWasCalled(t *testing.T) {
	got := partialBailoutNudge("manda whatsapp a +573214447235", map[string]bool{"whatsapp_send": true, "claw_schedule_reminder": true})
	if got != "" {
		t.Fatalf("expected no nudge when whatsapp_send was actually called, got %q", got)
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
