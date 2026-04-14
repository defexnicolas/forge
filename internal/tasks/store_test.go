package tasks

import (
	"strings"
	"testing"
)

func TestCreateListUpdateGet(t *testing.T) {
	store := New(t.TempDir())
	task, err := store.Create("write tests", "important")
	if err != nil {
		t.Fatal(err)
	}
	if task.ID == "" {
		t.Fatal("expected task id")
	}
	updated, err := store.Update(task.ID, "", "done", "")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != "completed" {
		t.Fatalf("expected completed, got %q", updated.Status)
	}
	got, err := store.Get(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "write tests" {
		t.Fatalf("unexpected task %#v", got)
	}
	list, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(Format(list), "write tests") {
		t.Fatalf("expected formatted task, got %q", Format(list))
	}
}

func TestReplacePlan(t *testing.T) {
	store := New(t.TempDir())
	plan, err := store.ReplacePlan([]string{"first", "", "second"})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(plan))
	}
	if plan[0].ID != "plan-1" || plan[1].ID != "plan-2" {
		t.Fatalf("unexpected plan ids %#v", plan)
	}
}
