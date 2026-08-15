package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/e-aleixandre/moa/pkg/core"
)

func TestSecretCommandRefusesNonAliasToken(t *testing.T) {
	m := newTestModel()
	updated, _ := m.handleCommand("secret api_key not/a-name")
	got := updated.(appModel)
	if got.secretPrompt.active {
		t.Fatal("/secret with a non-alias token opened the masked prompt")
	}
	if len(got.s.blocks) == 0 || !strings.Contains(got.s.blocks[len(got.s.blocks)-1].Raw, "Never type values") {
		t.Fatalf("refusal = %#v", got.s.blocks)
	}
	if strings.Contains(got.s.blocks[len(got.s.blocks)-1].Raw, "not/a-name") {
		t.Fatal("refusal echoed the potentially secret token")
	}
}

func TestSecretCommandIsDiscardedBeforeInputHistory(t *testing.T) {
	m := newTestModel()
	m.input.textarea.SetValue("/secret token not/a-name")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := updated.(appModel)
	if len(got.input.history) != 0 {
		t.Fatalf("secret command remained in history: %#v", got.input.history)
	}
	if got.input.textarea.Value() != "" || got.input.draft != "" {
		t.Fatalf("secret command remained in input state: value=%q draft=%q", got.input.textarea.Value(), got.input.draft)
	}
}

func TestSecretCommandValidAliasStartsPrompt(t *testing.T) {
	m := newTestModel()
	updated, _ := m.handleCommand("secret api_key")
	got := updated.(appModel)
	if !got.secretPrompt.active || got.secretPrompt.phase != secretValue {
		t.Fatal("valid /secret alias did not open the masked value prompt")
	}
}

func TestSecretCommandOnlyUsesFirstLineForAliases(t *testing.T) {
	for _, command := range []string{
		"secret token\nhunter2",
		"secret token\r\nhunter2",
	} {
		t.Run(strings.ReplaceAll(command, "\n", " newline "), func(t *testing.T) {
			m := newTestModel()
			m.input.textarea.SetValue("/" + command)
			updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			got := updated.(appModel)
			if !got.secretPrompt.active {
				t.Fatal("/secret did not open the masked prompt")
			}
			if len(got.input.history) != 0 || got.input.textarea.Value() != "" || got.input.draft != "" {
				t.Fatalf("secret command remained in input state: history=%#v value=%q draft=%q", got.input.history, got.input.textarea.Value(), got.input.draft)
			}
			if names := got.secretPrompt.names; len(names) != 1 || names[0] != "token" {
				t.Fatalf("secret aliases = %#v, want only token", names)
			}
			if view := got.secretPrompt.View(80, CatppuccinMocha); strings.Contains(view, "hunter2") {
				t.Fatalf("secret prompt echoed trailing line: %q", view)
			}
			for _, block := range got.s.blocks {
				if strings.Contains(block.Raw, "hunter2") {
					t.Fatalf("transcript block echoed trailing line: %#v", block)
				}
			}
		})
	}
}

func TestSecretPromptMasksAndWipesValue(t *testing.T) {
	var p secretPrompt
	p.Start([]string{"token"})
	p.Type("never-render-this")
	view := p.View(80, CatppuccinMocha)
	if strings.Contains(view, "never-render-this") {
		t.Fatal("secret value was rendered")
	}
	if !strings.Contains(view, "••••") {
		t.Fatal("masked prompt did not render bullets")
	}
	p.Cancel()
	if p.active || p.buffer != "" || len(p.entries) != 0 {
		t.Fatal("cancel did not wipe secret prompt state")
	}
}

func TestSecretPromptAliasErrorsDoNotEchoTypedTokenInStatus(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*secretPrompt)
		token string
	}{
		{
			name:  "invalid alias",
			token: "accidental-value!",
			setup: func(p *secretPrompt) { p.Start(nil) },
		},
		{
			name:  "duplicate alias",
			token: "already-used",
			setup: func(p *secretPrompt) {
				p.Start(nil)
				p.names = []string{"already-used"}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel()
			tc.setup(&m.secretPrompt)
			m.secretPrompt.Type(tc.token)
			updated, _ := m.handleSecretKey(tea.KeyMsg{Type: tea.KeyEnter})
			got := updated.(appModel)
			status := GetActiveLayout().RenderLiveNotice(got.s.pendingStatus, got.width, ActiveTheme)
			if strings.Contains(status, tc.token) {
				t.Fatalf("rendered status echoed typed token %q", tc.token)
			}
		})
	}
}

func TestSecretPromptCollectsInteractiveBatch(t *testing.T) {
	var p secretPrompt
	p.Start(nil)
	p.Type("db")
	if complete, err := p.Submit(); err != nil || complete {
		t.Fatalf("alias submit = (%v, %v), want (false, nil)", complete, err)
	}
	p.Type("s3cret")
	if complete, err := p.Submit(); err != nil || complete || p.phase != secretMore {
		t.Fatalf("value submit = (%v, %v), phase %v", complete, err, p.phase)
	}
	entries := p.Entries()
	if len(entries) != 1 || entries[0].Name != "db" || entries[0].Value != "s3cret" {
		t.Fatalf("entries = %#v", entries)
	}
}

func TestSecretPromptEscCancels(t *testing.T) {
	m := newTestModel()
	m.secretPrompt.Start([]string{"token"})
	m.secretPrompt.Type("value")
	updated, _ := m.handleSecretKey(tea.KeyMsg{Type: tea.KeyEsc})
	got := updated.(appModel)
	if got.secretPrompt.active || got.secretPrompt.buffer != "" {
		t.Fatal("Esc did not cancel and wipe masked input")
	}
}

func TestSecretBatchRestoresAsCardWithoutModelNote(t *testing.T) {
	m := newTestModel()
	note := "A secret batch is available in /tmp/private (aliases: db)."
	m.rebuildFromMessages([]core.AgentMessage{{
		Message: core.Message{Role: "user", Content: []core.Content{core.TextContent(note)}},
		Custom:  map[string]any{"source": "secret_batch", "secret_aliases": []string{"db"}},
	}})
	if len(m.s.blocks) != 1 || m.s.blocks[0].Type != "status" {
		t.Fatalf("blocks = %#v, want one status card", m.s.blocks)
	}
	if strings.Contains(m.s.blocks[0].Raw, note) || !strings.Contains(m.s.blocks[0].Raw, "db") {
		t.Fatalf("restored secret card = %q", m.s.blocks[0].Raw)
	}
}
