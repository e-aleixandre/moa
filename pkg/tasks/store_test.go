package tasks

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

func TestCreateAndProgress(t *testing.T) {
	s := NewStore()

	done, total := s.Progress()
	if total != 0 || done != 0 {
		t.Fatalf("expected 0/0, got %d/%d", done, total)
	}

	s.Create("Task A", "", nil)
	s.Create("Task B", "desc", []int{1})

	done, total = s.Progress()
	if total != 2 || done != 0 {
		t.Fatalf("expected 0/2, got %d/%d", done, total)
	}

	tasks := s.Tasks()
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(tasks))
	}
	if tasks[0].Title != "Task A" {
		t.Errorf("expected title 'Task A', got %q", tasks[0].Title)
	}
	if tasks[1].Description != "desc" {
		t.Errorf("expected description 'desc', got %q", tasks[1].Description)
	}
	if len(tasks[1].DependsOn) != 1 || tasks[1].DependsOn[0] != 1 {
		t.Errorf("expected depends_on [1], got %v", tasks[1].DependsOn)
	}
}

func TestMarkDone(t *testing.T) {
	s := NewStore()
	s.Create("A", "", nil)
	s.Create("B", "", nil)

	if !s.MarkDone(1) {
		t.Fatal("expected MarkDone(1) to return true")
	}
	if s.MarkDone(99) {
		t.Fatal("expected MarkDone(99) to return false")
	}

	tasks := s.Tasks()
	if tasks[0].Status != "done" {
		t.Errorf("expected task 1 done, got %q", tasks[0].Status)
	}
	if tasks[0].CompletedAt == 0 {
		t.Error("expected non-zero CompletedAt")
	}

	done, total := s.Progress()
	if done != 1 || total != 2 {
		t.Fatalf("expected 1/2, got %d/%d", done, total)
	}
}

func TestAllDone(t *testing.T) {
	s := NewStore()
	if s.AllDone() {
		t.Fatal("AllDone should be false with no tasks")
	}

	s.Create("A", "", nil)
	if s.AllDone() {
		t.Fatal("AllDone should be false with pending tasks")
	}

	s.MarkDone(1)
	if !s.AllDone() {
		t.Fatal("AllDone should be true when all done")
	}
}

func TestUpdate(t *testing.T) {
	s := NewStore()
	s.Create("Original", "old desc", nil)

	newTitle := "Updated"
	newDesc := "new desc"
	deps := []int{1, 2}
	if !s.Update(1, &newTitle, &newDesc, &deps) {
		t.Fatal("expected Update to return true")
	}

	task, ok := s.Get(1)
	if !ok {
		t.Fatal("expected to find task 1")
	}
	if task.Title != "Updated" {
		t.Errorf("expected title 'Updated', got %q", task.Title)
	}
	if task.Description != "new desc" {
		t.Errorf("expected desc 'new desc', got %q", task.Description)
	}
	if len(task.DependsOn) != 2 {
		t.Errorf("expected 2 deps, got %v", task.DependsOn)
	}

	if s.Update(99, nil, nil, nil) {
		t.Fatal("expected Update(99) to return false")
	}
}

func TestReset(t *testing.T) {
	s := NewStore()
	s.Create("A", "", nil)
	s.Create("B", "", nil)

	s.Reset()
	if _, total := s.Progress(); total != 0 {
		t.Fatalf("expected 0 tasks after reset, got %d", total)
	}
}

func TestWidgetMode(t *testing.T) {
	s := NewStore()
	if s.GetWidgetMode() != WidgetAll {
		t.Fatalf("expected default WidgetAll, got %q", s.GetWidgetMode())
	}

	s.SetWidgetMode(WidgetCurrent)
	if s.GetWidgetMode() != WidgetCurrent {
		t.Fatalf("expected WidgetCurrent, got %q", s.GetWidgetMode())
	}
}

func TestTimestamps(t *testing.T) {
	s := NewStore()
	s.Create("Timestamped", "", nil)

	tasks := s.Tasks()
	if tasks[0].CreatedAt == 0 {
		t.Error("expected non-zero CreatedAt")
	}

	s.MarkDone(1)
	tasks = s.Tasks()
	if tasks[0].CompletedAt == 0 {
		t.Error("expected non-zero CompletedAt")
	}
	if tasks[0].CompletedAt < tasks[0].CreatedAt {
		t.Error("CompletedAt should be >= CreatedAt")
	}
}

func TestSaveRestoreState(t *testing.T) {
	s := NewStore()
	s.Create("A", "desc", nil)
	s.MarkDone(1)
	s.SetWidgetMode(WidgetCurrent)

	meta := s.SaveToMetadata()

	s2 := NewStore()
	s2.RestoreFromMetadata(meta)

	tasks := s2.Tasks()
	if len(tasks) != 1 || tasks[0].Status != "done" {
		t.Fatalf("expected 1 done task, got %v", tasks)
	}
	if s2.GetWidgetMode() != WidgetCurrent {
		t.Fatalf("expected WidgetCurrent, got %q", s2.GetWidgetMode())
	}
}

func TestRestoreInvalidWidgetMode(t *testing.T) {
	s := NewStore()
	s.RestoreState(State{WidgetMode: "bogus"})
	if s.GetWidgetMode() != WidgetAll {
		t.Fatalf("expected WidgetAll for invalid mode, got %q", s.GetWidgetMode())
	}

	s2 := NewStore()
	s2.RestoreState(State{})
	if s2.GetWidgetMode() != WidgetAll {
		t.Fatalf("expected WidgetAll for empty mode, got %q", s2.GetWidgetMode())
	}
}

func TestConcurrentCreation(t *testing.T) {
	s := NewStore()

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			s.Create(fmt.Sprintf("Task %d", n), "", nil)
		}(i)
	}
	wg.Wait()

	tasks := s.Tasks()
	if len(tasks) != 20 {
		t.Fatalf("expected 20 tasks, got %d", len(tasks))
	}

	// Verify unique IDs.
	ids := make(map[int]bool)
	for _, task := range tasks {
		if ids[task.ID] {
			t.Fatalf("duplicate task ID: %d", task.ID)
		}
		ids[task.ID] = true
	}
}

func TestTool(t *testing.T) {
	s := NewStore()
	tool := NewTool(s)
	ctx := context.Background()

	// Create.
	r, err := tool.Execute(ctx, map[string]any{"action": "create", "title": "First"}, nil)
	if err != nil || r.IsError {
		t.Fatalf("create failed: err=%v, result=%v", err, r)
	}

	// List.
	r, err = tool.Execute(ctx, map[string]any{"action": "list"}, nil)
	if err != nil || r.IsError {
		t.Fatalf("list failed: err=%v, result=%v", err, r)
	}

	// Done.
	r, err = tool.Execute(ctx, map[string]any{"action": "done", "id": float64(1)}, nil)
	if err != nil || r.IsError {
		t.Fatalf("done failed: err=%v, result=%v", err, r)
	}

	// Get.
	r, err = tool.Execute(ctx, map[string]any{"action": "get", "id": float64(1)}, nil)
	if err != nil || r.IsError {
		t.Fatalf("get failed: err=%v, result=%v", err, r)
	}

	// Missing title for create.
	r, err = tool.Execute(ctx, map[string]any{"action": "create", "title": ""}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !r.IsError {
		t.Error("expected error for empty title")
	}

	// Unknown action.
	r, err = tool.Execute(ctx, map[string]any{"action": "nope"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !r.IsError {
		t.Error("expected error for unknown action")
	}
}
