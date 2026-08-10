package serve

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
)

const (
	defaultHistoryPageSize = 50
	maxHistoryPageSize     = 100
)

type historyResponse struct {
	Messages   []core.AgentMessage `json:"messages"`
	NextBefore string              `json:"next_before,omitempty"`
	HasMore    bool                `json:"has_more"`
}

// handleHistory returns chronological display history in backwards pages. Page
// size is an objective: a page may grow to keep an assistant and its contiguous
// tool results together.
func handleHistory(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit := defaultHistoryPageSize
		if raw := r.URL.Query().Get("limit"); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil || value < 1 || value > maxHistoryPageSize {
				http.Error(w, "invalid limit", http.StatusBadRequest)
				return
			}
			limit = value
		}
		messages, err := m.historyMessages(r.PathValue("id"))
		if errors.Is(err, ErrNotFound) || errors.Is(err, session.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, "history unavailable", http.StatusConflict)
			return
		}
		page, nextBefore, hasMore, ok := historyPage(messages, r.URL.Query().Get("before"), limit)
		if !ok {
			http.Error(w, "history anchor is no longer on this branch", http.StatusGone)
			return
		}
		writeJSON(w, http.StatusOK, historyResponse{Messages: sanitizeHistoryRange(page), NextBefore: nextBefore, HasMore: hasMore})
	}
}

func (m *Manager) historyMessages(id string) ([]core.AgentMessage, error) {
	if sess, ok := m.Get(id); ok {
		return bus.QueryTyped[bus.GetDisplayMessages, []core.AgentMessage](sess.runtime.Bus, bus.GetDisplayMessages{})
	}
	saved, _, err := session.FindSessionReadOnly(m.sessionBaseDir, id)
	if err != nil {
		return nil, err
	}
	if len(saved.Entries) == 0 {
		return saved.Messages, nil
	}
	tree, err := session.NewTreeFromEntries(saved.Entries, saved.LeafID)
	if err != nil {
		return nil, err
	}
	return tree.AllMessages(), nil
}

// historyPage returns a chronological page ending before before. An absent
// before selects the tail. A cursor absent from the current branch is invalid.
func historyPage(messages []core.AgentMessage, before string, limit int) ([]core.AgentMessage, string, bool, bool) {
	end := len(messages)
	if before != "" {
		end = -1
		for i, message := range messages {
			if message.MsgID == before {
				end = i
				break
			}
		}
		if end < 0 {
			return nil, "", false, false
		}
	}
	start := max(0, end-limit)
	start = historyPageStart(messages, end, start)
	if start >= end {
		return nil, "", false, true
	}
	page := messages[start:end]
	if start == 0 || page[0].MsgID == "" {
		return page, "", false, true
	}
	return page, page[0].MsgID, true, true
}
