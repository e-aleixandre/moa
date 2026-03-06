package tui

import (
	"strings"
	"sync"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func plainStyle() lipgloss.Style {
	return lipgloss.NewStyle()
}

func TestStatusLine_Empty(t *testing.T) {
	sl := NewStatusLine(plainStyle())
	if !sl.IsEmpty() {
		t.Fatal("should be empty")
	}
	if v := sl.View(80); v != "" {
		t.Fatalf("empty view should be empty string, got %q", v)
	}
}

func TestStatusLine_SetAndView(t *testing.T) {
	sl := NewStatusLine(plainStyle())
	sl.Set("a", "hello", 10)
	if sl.IsEmpty() {
		t.Fatal("should not be empty")
	}
	v := sl.View(80)
	if !strings.Contains(v, "hello") {
		t.Fatalf("view should contain 'hello', got %q", v)
	}
}

func TestStatusLine_Priority(t *testing.T) {
	sl := NewStatusLine(plainStyle())
	sl.Set("z", "last", 99)
	sl.Set("a", "first", 1)
	v := sl.View(200)
	idxFirst := strings.Index(v, "first")
	idxLast := strings.Index(v, "last")
	if idxFirst > idxLast {
		t.Fatalf("priority 1 should come before 99: %q", v)
	}
}

func TestStatusLine_Remove(t *testing.T) {
	sl := NewStatusLine(plainStyle())
	sl.Set("a", "hello", 10)
	sl.Remove("a")
	if !sl.IsEmpty() {
		t.Fatal("should be empty after remove")
	}
}

func TestStatusLine_SetEmptyRemoves(t *testing.T) {
	sl := NewStatusLine(plainStyle())
	sl.Set("a", "hello", 10)
	sl.Set("a", "", 10)
	if !sl.IsEmpty() {
		t.Fatal("setting empty text should remove segment")
	}
}

func TestStatusLine_Clear(t *testing.T) {
	sl := NewStatusLine(plainStyle())
	sl.Set("a", "one", 1)
	sl.Set("b", "two", 2)
	sl.Clear()
	if !sl.IsEmpty() {
		t.Fatal("should be empty after clear")
	}
}

func TestStatusLine_ConcurrentAccess(t *testing.T) {
	sl := NewStatusLine(plainStyle())
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			sl.Set("key", "val", n)
			sl.View(80)
			sl.IsEmpty()
		}(i)
	}
	wg.Wait()
}

func TestStatusLine_UpdateModelSegment(t *testing.T) {
	sl := NewStatusLine(plainStyle())
	sl.UpdateModelSegment("Claude Sonnet 4")
	v := sl.View(120)
	if !strings.Contains(v, "model") {
		t.Fatalf("should contain 'model': %q", v)
	}
	if !strings.Contains(v, "Claude Sonnet 4") {
		t.Fatalf("should contain model name: %q", v)
	}
}

func TestStatusLine_UpdateContextSegment(t *testing.T) {
	sl := NewStatusLine(plainStyle())
	sl.UpdateContextSegment(42)
	v := sl.View(120)
	if !strings.Contains(v, "42%") {
		t.Fatalf("should contain '42%%': %q", v)
	}
}

func TestStatusLine_ContextClamp(t *testing.T) {
	sl := NewStatusLine(plainStyle())
	sl.UpdateContextSegment(-10)
	v := sl.View(120)
	if !strings.Contains(v, "0%") {
		t.Fatalf("should clamp to 0%%: %q", v)
	}
	sl.UpdateContextSegment(200)
	v = sl.View(120)
	if !strings.Contains(v, "100%") {
		t.Fatalf("should clamp to 100%%: %q", v)
	}
}
