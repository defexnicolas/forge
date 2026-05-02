package tasks

import (
	"strings"
	"testing"
)

func TestCreateListUpdateGet(t *testing.T) {
	store := New(t.TempDir())
	t.Cleanup(func() { store.Close() })
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
	t.Cleanup(func() { store.Close() })
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

func TestClearRemovesTasks(t *testing.T) {
	store := New(t.TempDir())
	t.Cleanup(func() { store.Close() })
	if _, err := store.ReplacePlan([]string{"first", "second"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Clear(); err != nil {
		t.Fatal(err)
	}
	list, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("expected cleared tasks, got %#v", list)
	}
}

func TestReplacePlanRejectsAccidentalEmptyOverwrite(t *testing.T) {
	store := New(t.TempDir())
	t.Cleanup(func() { store.Close() })
	if _, err := store.ReplacePlan([]string{"first"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReplacePlan(nil); err == nil {
		t.Fatal("expected empty overwrite to be rejected")
	}
	list, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Title != "first" {
		t.Fatalf("expected existing task preserved, got %#v", list)
	}
}

func TestUpdateByTitleFallback(t *testing.T) {
	store := New(t.TempDir())
	t.Cleanup(func() { store.Close() })
	if _, err := store.ReplacePlan([]string{"Wire handlers", "Add CSS task"}); err != nil {
		t.Fatal(err)
	}
	updated, err := store.Update("", "Wire handlers", "in_progress", "refined")
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != "plan-1" || updated.Status != "in_progress" || updated.Notes != "refined" {
		t.Fatalf("unexpected updated task %#v", updated)
	}
}
