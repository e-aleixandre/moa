package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"github.com/e-aleixandre/moa/pkg/askuser"
	"github.com/e-aleixandre/moa/pkg/bus"
)

// ForYouItem is one thing a session is waiting on the user for: an open
// question (asynchronous or blocking), a tool permission or a password. It is
// a read-only view of objects that already exist; every answer goes through
// the session's own endpoints (/ask, /questions/{id}/dismiss, /permission),
// and a password is only ever entered in the session's secure sheet.
type ForYouItem struct {
	Kind      string    `json:"kind"` // question | permission | secret
	ID        string    `json:"id"`
	SessionID string    `json:"session_id"`
	Since     time.Time `json:"since"`
	// Blocking: the session has stopped until this is answered. An
	// asynchronous question does not block; its session keeps working.
	Blocking    bool               `json:"blocking"`
	Async       bool               `json:"async,omitempty"`
	RequiresTap bool               `json:"requires_tap,omitempty"`
	Questions   []askuser.Question `json:"questions,omitempty"`
	// Permission: the tool and the exact command or arguments it asked for.
	Tool string         `json:"tool,omitempty"`
	Args map[string]any `json:"args,omitempty"`
	// Secret: what the password is for, never a value.
	Label   string `json:"label,omitempty"`
	Command string `json:"command,omitempty"`
}

// openQuestionIDsBySession maps each session to its open asynchronous
// questions. Best effort: without a question store there is nothing open.
func (m *Manager) openQuestionIDsBySession() map[string][]string {
	if m.tasks == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	qs, err := m.tasks.AllOpenQuestions(ctx)
	if err != nil || len(qs) == 0 {
		return nil
	}
	out := make(map[string][]string, len(qs))
	for _, q := range qs {
		out[q.SessionID] = append(out[q.SessionID], q.ID)
	}
	return out
}

// ForYou lists everything waiting on the user across every session, oldest
// first. Blocking requests come from the loaded sessions' live state; open
// asynchronous questions come from the question store, so they are listed
// whether or not their session is loaded and survive a restart.
func (m *Manager) ForYou(ctx context.Context) []ForYouItem {
	items := []ForYouItem{}
	if m.tasks != nil {
		if qs, err := m.tasks.AllOpenQuestions(ctx); err == nil {
			for _, q := range qs {
				items = append(items, ForYouItem{
					Kind: "question", ID: q.ID, SessionID: q.SessionID,
					Since: time.UnixMilli(q.AskedAt), Async: true,
					RequiresTap: q.RequiresTap, Questions: q.Questions,
				})
			}
		}
	}
	m.mu.RLock()
	loaded := make([]*ManagedSession, 0, len(m.sessions))
	for _, s := range m.sessions {
		if s != nil {
			loaded = append(loaded, s)
		}
	}
	m.mu.RUnlock()
	for _, s := range loaded {
		b := s.runtime.Bus
		if pending, err := bus.QueryTyped[bus.GetPendingApproval, bus.PendingApprovalInfo](b, bus.GetPendingApproval{}); err == nil {
			if a := pending.Ask; a != nil && !a.Async {
				qs := make([]askuser.Question, 0, len(a.Questions))
				for _, q := range a.Questions {
					qs = append(qs, askuser.Question{Text: q.Text, Options: q.Options, Multiple: q.Multiple})
				}
				items = append(items, ForYouItem{
					Kind: "question", ID: a.ID, SessionID: s.ID, Since: a.RequestedAt,
					Blocking: true, RequiresTap: a.RequiresTap, Questions: qs,
				})
			}
			if p := pending.Permission; p != nil {
				items = append(items, ForYouItem{
					Kind: "permission", ID: p.ID, SessionID: s.ID, Since: p.RequestedAt,
					Blocking: true, Tool: p.ToolName, Args: p.Args,
				})
			}
		}
		if broker := s.runtime.Context().SecretBroker; broker != nil {
			for _, req := range broker.Pending() {
				label := ""
				if len(req.Fields) > 0 {
					label = req.Fields[0].Label
				}
				items = append(items, ForYouItem{
					Kind: "secret", ID: req.ID, SessionID: s.ID, Since: req.RequestedAt,
					Blocking: true, Label: label, Command: req.Command,
				})
			}
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Since.Before(items[j].Since) })
	return items
}

// handleForYou serves GET /api/for-you: read-only, additive.
func handleForYou(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body := map[string]any{"items": m.ForYou(r.Context())}
		if _, err := json.Marshal(body); err != nil {
			http.Error(w, "encode for-you: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, body)
	}
}
