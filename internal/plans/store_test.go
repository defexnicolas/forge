package plans

import (
	"strings"
	"testing"
)

func TestSaveAndCurrentPlan(t *testing.T) {
	store := New(t.TempDir())
	t.Cleanup(func() { store.Close() })

	doc, err := store.Save(Document{
		Summary:     "separate plan from todos",
		Context:     "plan panel is compact",
		Assumptions: []string{"tasks remain executable"},
		Approach:    "write a full plan document and derive a checklist",
		Stubs:       []string{"plan_write tool", "plan_get tool"},
		Risks:       []string{"model may skip checklist"},
		Validation:  []string{"go test ./..."},
	})
	if err != nil {
		t.Fatal(err)
	}
	if doc.ID != currentPlanID {
		t.Fatalf("plan id = %q", doc.ID)
	}

	got, ok, err := store.Current()
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected current plan")
	}
	if got.Summary != "separate plan from todos" || len(got.Stubs) != 2 {
		t.Fatalf("unexpected plan %#v", got)
	}
	formatted := Format(got)
	for _, want := range []string{"Summary:", "Approach:", "Stubs:", "plan_write tool", "Validation:"} {
		if !strings.Contains(formatted, want) {
			t.Fatalf("formatted plan missing %q:\n%s", want, formatted)
		}
	}
}

func TestSavePlanRequiresContent(t *testing.T) {
	store := New(t.TempDir())
	t.Cleanup(func() { store.Close() })

	if _, err := store.Save(Document{}); err == nil {
		t.Fatal("expected empty plan to be rejected")
	}
}
