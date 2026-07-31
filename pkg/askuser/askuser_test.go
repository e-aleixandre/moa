package askuser

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestAskUser_SingleQuestion(t *testing.T) {
	bridge := NewBridge()
	tool := NewTool(bridge)

	go func() {
		p := <-bridge.Prompts()
		if len(p.Questions) != 1 {
			t.Errorf("expected 1 question, got %d", len(p.Questions))
		}
		if p.Questions[0].Text != "What color?" {
			t.Errorf("question: got %q", p.Questions[0].Text)
		}
		p.Response <- []string{"blue"}
	}()

	result, err := tool.Execute(context.Background(), map[string]any{
		"questions": []any{
			map[string]any{"question": "What color?"},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}
	if result.Content[0].Text != "blue" {
		t.Errorf("answer: got %q, want %q", result.Content[0].Text, "blue")
	}
}

func TestAskUser_MultipleQuestions(t *testing.T) {
	bridge := NewBridge()
	tool := NewTool(bridge)

	go func() {
		p := <-bridge.Prompts()
		if len(p.Questions) != 2 {
			t.Errorf("expected 2 questions, got %d", len(p.Questions))
		}
		p.Response <- []string{"PostgreSQL", "5432"}
	}()

	result, err := tool.Execute(context.Background(), map[string]any{
		"questions": []any{
			map[string]any{
				"question": "What database?",
				"options":  []any{"PostgreSQL", "MySQL", "SQLite"},
			},
			map[string]any{
				"question": "What port?",
			},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}
	text := result.Content[0].Text
	if !strings.Contains(text, "PostgreSQL") || !strings.Contains(text, "5432") {
		t.Errorf("expected both answers in result, got: %s", text)
	}
}

func TestAskUser_WithOptions(t *testing.T) {
	bridge := NewBridge()
	tool := NewTool(bridge)

	go func() {
		p := <-bridge.Prompts()
		q := p.Questions[0]
		if len(q.Options) != 3 {
			t.Errorf("expected 3 options, got %d", len(q.Options))
		}
		p.Response <- []string{"MySQL"}
	}()

	result, err := tool.Execute(context.Background(), map[string]any{
		"questions": []any{
			map[string]any{
				"question": "Database?",
				"options":  []any{"PostgreSQL", "MySQL", "SQLite"},
			},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Content[0].Text != "MySQL" {
		t.Errorf("got %q, want %q", result.Content[0].Text, "MySQL")
	}
}

func TestAskUser_EmptyQuestions(t *testing.T) {
	bridge := NewBridge()
	tool := NewTool(bridge)

	result, err := tool.Execute(context.Background(), map[string]any{
		"questions": []any{},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("expected error for empty questions array")
	}
}

func TestAskUser_MissingQuestions(t *testing.T) {
	bridge := NewBridge()
	tool := NewTool(bridge)

	result, err := tool.Execute(context.Background(), map[string]any{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("expected error for missing questions")
	}
}

func TestAskUser_EmptyQuestionText(t *testing.T) {
	bridge := NewBridge()
	tool := NewTool(bridge)

	result, err := tool.Execute(context.Background(), map[string]any{
		"questions": []any{
			map[string]any{"question": ""},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("expected error for empty question text")
	}
}

// A batch bigger than MaxQuestions is rejected where the prompt is created, so
// it can never reach a UI (or the Automation API's 32-answer cap) unanswerable.
func TestAskUser_TooManyQuestions(t *testing.T) {
	bridge := NewBridge()
	tool := NewTool(bridge)

	questions := make([]any, MaxQuestions+1)
	for i := range questions {
		questions[i] = map[string]any{"question": "q"}
	}
	result, err := tool.Execute(context.Background(), map[string]any{"questions": questions}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("expected error for too many questions")
	}
	if !strings.Contains(result.Content[0].Text, "too many questions") {
		t.Fatalf("error = %q, want it to mention the cap", result.Content[0].Text)
	}
}

// Exactly MaxQuestions still works: the cap is inclusive.
func TestAskUser_MaxQuestionsAccepted(t *testing.T) {
	bridge := NewBridge()
	tool := NewTool(bridge)

	questions := make([]any, MaxQuestions)
	answers := make([]string, MaxQuestions)
	for i := range questions {
		questions[i] = map[string]any{"question": "q"}
		answers[i] = "a"
	}
	go func() {
		p := <-bridge.Prompts()
		p.Response <- answers
	}()
	result, err := tool.Execute(context.Background(), map[string]any{"questions": questions}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}
}

func TestAskUser_OversizedTextRejected(t *testing.T) {
	bridge := NewBridge()
	tool := NewTool(bridge)

	tests := []struct {
		name   string
		params map[string]any
	}{
		{"question text", map[string]any{"questions": []any{
			map[string]any{"question": strings.Repeat("x", MaxQuestionBytes+1)},
		}}},
		{"option text", map[string]any{"questions": []any{
			map[string]any{"question": "q", "options": []any{strings.Repeat("x", MaxOptionBytes+1)}},
		}}},
		{"too many options", map[string]any{"questions": []any{
			map[string]any{"question": "q", "options": make([]any, MaxOptions+1)},
		}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tool.Execute(context.Background(), tt.params, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !result.IsError {
				t.Fatal("expected an error result")
			}
		})
	}
}

func TestAskUser_CancelledBeforeSend(t *testing.T) {
	bridge := NewBridge()
	tool := NewTool(bridge)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := tool.Execute(ctx, map[string]any{
		"questions": []any{map[string]any{"question": "hello?"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("expected error for cancelled context")
	}
}

func TestAskUser_CancelledWhileWaiting(t *testing.T) {
	bridge := NewBridge()
	tool := NewTool(bridge)

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		<-bridge.Prompts()
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	result, err := tool.Execute(ctx, map[string]any{
		"questions": []any{map[string]any{"question": "hello?"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("expected error for cancelled context")
	}
}

func TestFormatAnswers_Single(t *testing.T) {
	questions := []Question{{Text: "Color?"}}
	result := formatAnswers(questions, []string{"blue"})
	if result.Content[0].Text != "blue" {
		t.Errorf("got %q", result.Content[0].Text)
	}
}

func TestFormatAnswers_Multiple(t *testing.T) {
	questions := []Question{
		{Text: "DB?"},
		{Text: "Port?"},
	}
	result := formatAnswers(questions, []string{"pg", "5432"})
	text := result.Content[0].Text
	if !strings.Contains(text, "Q: DB?") || !strings.Contains(text, "A: pg") {
		t.Errorf("unexpected format: %s", text)
	}
	if !strings.Contains(text, "Q: Port?") || !strings.Contains(text, "A: 5432") {
		t.Errorf("unexpected format: %s", text)
	}
}

func TestFormatAnswers_ShortSlice(t *testing.T) {
	// An aborted prompt can resolve with fewer answers than questions (or none).
	// formatAnswers must render blanks instead of panicking on an out-of-range index.
	questions := []Question{{Text: "DB?"}, {Text: "Port?"}}
	result := formatAnswers(questions, nil)
	text := result.Content[0].Text
	if !strings.Contains(text, "Q: DB?\nA: ") || !strings.Contains(text, "Q: Port?\nA: ") {
		t.Errorf("expected blank answers, got %q", text)
	}
	// Single-question path with no answers must not panic either.
	if got := formatAnswers([]Question{{Text: "x"}}, nil).Content[0].Text; got != "" {
		t.Errorf("single-question empty answers = %q, want \"\"", got)
	}
}
