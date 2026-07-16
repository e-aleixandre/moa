package serve

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/session"
	"github.com/ealeixandre/moa/pkg/subagent"
)

// SubagentSummary describes a subagent available to an owner-authorized
// client, including the delegated task.
type SubagentSummary struct {
	JobID      string    `json:"job_id"`
	Task       string    `json:"task"`
	Model      string    `json:"model,omitempty"`
	Status     string    `json:"status"`
	Async      bool      `json:"async"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	Source     string    `json:"source"`
}

type subagentListResponse struct {
	SessionID string            `json:"session_id"`
	Subagents []SubagentSummary `json:"subagents"`
}

// subagentConversationResponse is the paginated transcript projection. It
// includes messages and bounded tool-call arguments, while tool result output
// remains outside the default response budget.
type subagentConversationResponse struct {
	SessionID  string                `json:"session_id"`
	JobID      string                `json:"job_id"`
	Task       string                `json:"task"`
	Model      string                `json:"model,omitempty"`
	Status     string                `json:"status"`
	Async      bool                  `json:"async"`
	StartedAt  time.Time             `json:"started_at,omitempty"`
	FinishedAt time.Time             `json:"finished_at,omitempty"`
	Source     string                `json:"source"`
	Order      string                `json:"order"`
	Messages   []ConversationMessage `json:"messages"`
	NextCursor string                `json:"next_cursor,omitempty"`
	HasMore    bool                  `json:"has_more"`
}

type subagentConversationSnapshot struct {
	summary     SubagentSummary
	messages    []ConversationMessage
	toolDetails map[string]conversationToolDetail
}

func handleSubagentConversation(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		detail := r.URL.Query().Get("detail")
		if detail != "" && detail != "full" {
			http.Error(w, "invalid detail", http.StatusBadRequest)
			return
		}
		limit, err := conversationLimit(r)
		if err != nil {
			http.Error(w, "invalid limit", http.StatusBadRequest)
			return
		}
		snapshot, err := m.subagentConversationSnapshot(r.PathValue("id"), r.PathValue("jobID"))
		if errors.Is(err, ErrNotFound) || errors.Is(err, session.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, "subagent conversation unavailable", http.StatusConflict)
			return
		}
		if detail == "full" {
			itemID := r.URL.Query().Get("item_id")
			if itemID == "" {
				http.Error(w, "item_id is required", http.StatusBadRequest)
				return
			}
			toolDetail, ok := snapshot.toolDetails[itemID]
			if !ok {
				http.Error(w, "tool item not found", http.StatusNotFound)
				return
			}
			writeJSON(w, http.StatusOK, conversationToolDetailResponse{Output: toolDetail.output, Truncated: toolDetail.truncated})
			return
		}
		beforeID := ""
		if raw := r.URL.Query().Get("cursor"); raw != "" {
			cursor, err := m.decodeConversationCursor(raw)
			if err != nil || cursor.SessionID != r.PathValue("id") || cursor.BeforeID == "" || cursor.Scope != subagentCursorScope(snapshot.summary.JobID) {
				http.Error(w, "invalid cursor", http.StatusBadRequest)
				return
			}
			beforeID = cursor.BeforeID
		}
		page, nextBefore, hasMore, ok := conversationPage(snapshot.messages, beforeID, limit)
		if !ok {
			http.Error(w, "invalid cursor", http.StatusBadRequest)
			return
		}
		response := subagentConversationResponse{
			SessionID: r.PathValue("id"), JobID: snapshot.summary.JobID,
			Task:  snapshot.summary.Task,
			Model: snapshot.summary.Model, Status: snapshot.summary.Status, Async: snapshot.summary.Async,
			StartedAt: snapshot.summary.StartedAt, FinishedAt: snapshot.summary.FinishedAt,
			Source: snapshot.summary.Source, Order: "newest_first", Messages: page, HasMore: hasMore,
		}
		if hasMore {
			response.NextCursor, err = m.encodeConversationCursor(conversationCursor{SessionID: response.SessionID, BeforeID: nextBefore, Scope: subagentCursorScope(response.JobID)})
			if err != nil {
				http.Error(w, "subagent conversation unavailable", http.StatusServiceUnavailable)
				return
			}
		}
		writeJSON(w, http.StatusOK, response)
	}
}

func handleSubagentList(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		items, err := m.subagentSummaries(r.PathValue("id"))
		if errors.Is(err, ErrNotFound) || errors.Is(err, session.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, "subagents unavailable", http.StatusConflict)
			return
		}
		writeJSON(w, http.StatusOK, subagentListResponse{SessionID: r.PathValue("id"), Subagents: items})
	}
}

func conversationLimit(r *http.Request) (int, error) {
	limit := defaultConversationPageSize
	if raw := r.URL.Query().Get("limit"); raw != "" {
		var err error
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > maxConversationPageSize {
			return 0, fmt.Errorf("invalid limit")
		}
	}
	return limit, nil
}

func subagentCursorScope(jobID string) string { return "subagent:" + jobID }

func (m *Manager) subagentConversationSnapshot(sessionID, jobID string) (subagentConversationSnapshot, error) {
	if sess, ok := m.Get(sessionID); ok {
		if sess.subagents != nil {
			for _, info := range sess.subagents.Snapshot() {
				if info.JobID == jobID {
					projection := safeSubagentConversationMessages(sess.subagents.Messages(jobID))
					return subagentConversationSnapshot{summary: subagentSummaryFromLive(info), messages: projection.messages, toolDetails: projection.toolDetails}, nil
				}
			}
		}
		if sess.persister != nil {
			if store := sess.persister.subagentStore(sessionID); store != nil {
				if transcript, err := store.Load(jobID); err == nil {
					projection := safeSubagentConversationMessages(transcript.Messages)
					return subagentConversationSnapshot{summary: subagentSummaryFromTranscript(*transcript), messages: projection.messages, toolDetails: projection.toolDetails}, nil
				} else if !errors.Is(err, session.ErrNotFound) {
					return subagentConversationSnapshot{}, err
				}
			}
		}
		return subagentConversationSnapshot{}, ErrNotFound
	}
	_, store, err := session.FindSessionReadOnly(m.sessionBaseDir, sessionID)
	if err != nil {
		return subagentConversationSnapshot{}, err
	}
	transcript, err := session.NewSubagentStore(store.Dir(), sessionID).Load(jobID)
	if err != nil {
		return subagentConversationSnapshot{}, err
	}
	projection := safeSubagentConversationMessages(transcript.Messages)
	return subagentConversationSnapshot{summary: subagentSummaryFromTranscript(*transcript), messages: projection.messages, toolDetails: projection.toolDetails}, nil
}

func (m *Manager) subagentSummaries(sessionID string) ([]SubagentSummary, error) {
	if sess, ok := m.Get(sessionID); ok {
		items := make(map[string]SubagentSummary)
		if sess.persister != nil {
			if store := sess.persister.subagentStore(sessionID); store != nil {
				transcripts, err := store.List()
				if err != nil {
					return nil, err
				}
				for _, transcript := range transcripts {
					items[transcript.JobID] = subagentSummaryFromTranscript(transcript)
				}
			}
		}
		if sess.subagents != nil {
			for _, info := range sess.subagents.Snapshot() {
				items[info.JobID] = subagentSummaryFromLive(info)
			}
		}
		return sortedSubagentSummaries(items), nil
	}
	_, store, err := session.FindSessionReadOnly(m.sessionBaseDir, sessionID)
	if err != nil {
		return nil, err
	}
	transcripts, err := session.NewSubagentStore(store.Dir(), sessionID).List()
	if err != nil {
		return nil, err
	}
	items := make(map[string]SubagentSummary, len(transcripts))
	for _, transcript := range transcripts {
		items[transcript.JobID] = subagentSummaryFromTranscript(transcript)
	}
	return sortedSubagentSummaries(items), nil
}

func subagentSummaryFromLive(info subagent.JobInfo) SubagentSummary {
	return SubagentSummary{JobID: info.JobID, Task: info.Task, Model: info.Model, Status: info.Status, Async: info.Async, StartedAt: info.StartedAt, FinishedAt: info.FinishedAt, Source: "active"}
}

func subagentSummaryFromTranscript(transcript session.SubagentTranscript) SubagentSummary {
	return SubagentSummary{JobID: transcript.JobID, Task: transcript.Task, Model: transcript.Model, Status: transcript.Status, Async: transcript.Async, StartedAt: transcript.StartedAt, FinishedAt: transcript.FinishedAt, Source: "persisted"}
}

func sortedSubagentSummaries(items map[string]SubagentSummary) []SubagentSummary {
	out := make([]SubagentSummary, 0, len(items))
	for _, item := range items {
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		left, right := out[i], out[j]
		if left.FinishedAt.Equal(right.FinishedAt) {
			return left.JobID < right.JobID
		}
		return left.FinishedAt.After(right.FinishedAt)
	})
	return out
}

// safeSubagentConversationMessages projects the durable/live child history
// into visible user/assistant text and bounded tool activity. It never
// serializes raw core.AgentMessage fields or tool-result text.
func safeSubagentConversationMessages(messages []core.AgentMessage) conversationProjection {
	out := make([]ConversationMessage, 0, len(messages))
	details := make(map[string]conversationToolDetail)
	results := make(map[string]core.AgentMessage)
	for _, msg := range messages {
		if msg.Role == "tool_result" && msg.ToolCallID != "" {
			if _, exists := results[msg.ToolCallID]; !exists {
				results[msg.ToolCallID] = msg
			}
		}
	}
	seenIDs := make(map[string]int, len(messages))
	for index, msg := range messages {
		if (msg.Role != "user" && msg.Role != "assistant") || len(msg.Custom) != 0 {
			continue
		}
		id := msg.MsgID
		if id == "" {
			id = fmt.Sprintf("legacy-%d", index)
		}
		baseID := id
		if duplicate := seenIDs[baseID]; duplicate > 0 {
			id = fmt.Sprintf("%s~%d", baseID, duplicate)
		}
		seenIDs[baseID]++
		text, omitted, truncated := safeDisplayText(msg.Content)
		if shouldShowConversationMessage(msg.Role, text, omitted, msg.Content) {
			item := ConversationMessage{ID: id, Role: msg.Role, Text: text, Omitted: omitted, Truncated: truncated, Timestamp: conversationTimestamp(msg.Timestamp)}
			for _, block := range msg.Content {
				if block.Type != "text" && block.Type != "tool_call" {
					item.OmittedBlocks++
				}
			}
			out = append(out, item)
		}
		if msg.Role != "assistant" {
			continue
		}
		for blockIndex, block := range msg.Content {
			if block.Type != "tool_call" {
				continue
			}
			action, target := conversationToolActivity(block.ToolName, block.Arguments)
			toolID := fmt.Sprintf("tool:%s:%d", id, blockIndex)
			item := ConversationMessage{ID: toolID, Role: "tool", Tool: block.ToolName, Action: action, Target: target, Status: "pending", Timestamp: conversationTimestamp(msg.Timestamp)}
			if result, found := results[block.ToolCallID]; found {
				item.Status = conversationToolStatus(result)
				if result.Timestamp > 0 {
					item.Timestamp = conversationTimestamp(result.Timestamp)
				}
				details[toolID] = conversationToolDetailFromResult(result)
			} else {
				details[toolID] = conversationToolDetail{}
			}
			out = append(out, item)
		}
	}
	return conversationProjection{messages: out, toolDetails: details}
}
