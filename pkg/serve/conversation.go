package serve

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ealeixandre/moa/pkg/bus"
	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/session"
)

const (
	defaultConversationPageSize     = 50
	maxConversationPageSize         = 100
	maxConversationTextBytes        = 12 << 10
	maxConversationToolDetailBytes  = 16 << 10
	maxConversationToolSummaryBytes = 512
)

// ConversationMessage is the owner-facing transcript DTO. Tool activity is
// projected into role=tool items; tool result output remains available only
// through the explicit detail query.
type ConversationMessage struct {
	ID            string    `json:"id"`
	Role          string    `json:"role"`
	Timestamp     time.Time `json:"timestamp,omitempty"`
	Text          string    `json:"text,omitempty"`
	Truncated     bool      `json:"truncated,omitempty"`
	Omitted       bool      `json:"omitted,omitempty"`
	OmittedBlocks int       `json:"omitted_blocks,omitempty"`
	Tool          string    `json:"tool,omitempty"`
	Summary       string    `json:"summary,omitempty"`
	Status        string    `json:"status,omitempty"`
}

type conversationBranch struct {
	LeafID string `json:"leaf_id,omitempty"`
	Source string `json:"source"` // active or saved
}

type conversationResponse struct {
	SessionID  string                `json:"session_id"`
	Title      string                `json:"title"`
	Branch     conversationBranch    `json:"branch"`
	Order      string                `json:"order"`
	Messages   []ConversationMessage `json:"messages"`
	NextCursor string                `json:"next_cursor,omitempty"`
	HasMore    bool                  `json:"has_more"`
}

type conversationSnapshot struct {
	id          string
	title       string
	leafID      string
	source      string
	messages    []ConversationMessage
	toolDetails map[string]conversationToolDetail
}

type conversationToolDetail struct {
	output    string
	truncated bool
}

type conversationToolDetailResponse struct {
	Output    string `json:"output"`
	Truncated bool   `json:"truncated,omitempty"`
}

type conversationProjection struct {
	messages    []ConversationMessage
	toolDetails map[string]conversationToolDetail
}

type conversationCursor struct {
	SessionID string `json:"s"`
	BeforeID  string `json:"b"`
}

func handleConversationMessages(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		detail := r.URL.Query().Get("detail")
		if detail != "" && detail != "full" {
			http.Error(w, "invalid detail", http.StatusBadRequest)
			return
		}
		limit := defaultConversationPageSize
		if raw := r.URL.Query().Get("limit"); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil || value < 1 || value > maxConversationPageSize {
				http.Error(w, "invalid limit", http.StatusBadRequest)
				return
			}
			limit = value
		}
		snapshot, err := m.conversationSnapshot(r.PathValue("id"))
		if errors.Is(err, ErrNotFound) || errors.Is(err, session.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, "conversation unavailable", http.StatusConflict)
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
			if err != nil || cursor.SessionID != snapshot.id || cursor.BeforeID == "" {
				http.Error(w, "invalid cursor", http.StatusBadRequest)
				return
			}
			beforeID = cursor.BeforeID
		}
		messages, nextBefore, hasMore, ok := conversationPage(snapshot.messages, beforeID, limit)
		if !ok {
			http.Error(w, "invalid cursor", http.StatusBadRequest)
			return
		}
		response := conversationResponse{
			SessionID: snapshot.id,
			Title:     snapshot.title,
			Branch:    conversationBranch{LeafID: snapshot.leafID, Source: snapshot.source},
			Order:     "newest_first",
			Messages:  messages,
			HasMore:   hasMore,
		}
		if response.HasMore {
			response.NextCursor, err = m.encodeConversationCursor(conversationCursor{SessionID: snapshot.id, BeforeID: nextBefore})
			if err != nil {
				http.Error(w, "conversation unavailable", http.StatusServiceUnavailable)
				return
			}
		}
		writeJSON(w, http.StatusOK, response)
	}
}

// conversationPage returns a newest-first page. A cursor's BeforeID is the
// oldest item from the prior page, so later pages are strictly older even when
// live messages append after the first request. If a branch no longer contains
// that anchor, the cursor is invalid rather than risking a gap or reordering.
func conversationPage(messages []ConversationMessage, beforeID string, limit int) ([]ConversationMessage, string, bool, bool) {
	end := len(messages)
	if beforeID != "" {
		end = -1
		for i, message := range messages {
			if message.ID == beforeID {
				end = i
				break
			}
		}
		if end < 0 {
			return nil, "", false, false
		}
	}
	start := max(0, end-limit)
	page := make([]ConversationMessage, 0, end-start)
	for i := end - 1; i >= start; i-- {
		page = append(page, messages[i])
	}
	if start == 0 {
		return page, "", false, true
	}
	return page, messages[start].ID, true, true
}

func (m *Manager) conversationSnapshot(id string) (conversationSnapshot, error) {
	if sess, ok := m.Get(id); ok {
		msgs, err := bus.QueryTyped[bus.GetDisplayMessages, []core.AgentMessage](sess.runtime.Bus, bus.GetDisplayMessages{})
		if err != nil {
			return conversationSnapshot{}, err
		}
		projection := safeConversationMessages(msgs)
		return conversationSnapshot{id: sess.ID, title: sess.title(), leafID: sess.runtime.Context().Tree.LeafID(), source: "active", messages: projection.messages, toolDetails: projection.toolDetails}, nil
	}

	// This intentionally does not call ResumeSession or session.FindSession:
	// both can initialize state and the latter can migrate legacy files.
	saved, _, err := session.FindSessionReadOnly(m.sessionBaseDir, id)
	if err != nil {
		return conversationSnapshot{}, err
	}
	projection, leaf, err := savedConversationMessages(saved)
	if err != nil {
		return conversationSnapshot{}, err
	}
	return conversationSnapshot{id: saved.ID, title: saved.Title, leafID: leaf, source: "saved", messages: projection.messages, toolDetails: projection.toolDetails}, nil
}

func savedConversationMessages(saved *session.Session) (conversationProjection, string, error) {
	if len(saved.Entries) == 0 {
		return safeConversationMessages(saved.Messages), "", nil
	}
	tree, err := session.NewTreeFromEntries(saved.Entries, saved.LeafID)
	if err != nil {
		return conversationProjection{}, "", err
	}
	return safeConversationMessages(tree.AllMessages()), tree.LeafID(), nil
}

func safeConversationMessages(messages []core.AgentMessage) conversationProjection {
	out := make([]ConversationMessage, 0, len(messages))
	details := make(map[string]conversationToolDetail)
	results := make(map[string]core.AgentMessage)
	for _, msg := range messages {
		if msg.Role == "tool_result" && msg.ToolCallID != "" {
			// A tool call ID is expected to be unique. Retaining the first result
			// also gives malformed legacy transcripts deterministic behavior.
			if _, exists := results[msg.ToolCallID]; !exists {
				results[msg.ToolCallID] = msg
			}
		}
	}
	seenIDs := make(map[string]int, len(messages))
	for index, msg := range messages {
		// Custom messages are extensions (shell, subagent and internal injected
		// notifications), not owner-authored display conversation.
		if (msg.Role != "user" && msg.Role != "assistant") || len(msg.Custom) != 0 {
			continue
		}
		id := msg.MsgID
		if id == "" {
			// Legacy flat sessions can predate MsgID. This synthetic, path-local
			// ID is deterministic without modifying the persisted transcript.
			id = fmt.Sprintf("legacy-%d", index)
		}
		baseID := id
		if duplicate := seenIDs[baseID]; duplicate > 0 {
			// Corrupt/legacy transcripts may have repeated MsgIDs. Keep the wire
			// cursor anchor unambiguous without modifying persisted state.
			id = fmt.Sprintf("%s~%d", baseID, duplicate)
		}
		seenIDs[baseID]++
		text, omitted, truncated := safeDisplayText(msg.Content)
		if strings.TrimSpace(text) == "" && !omitted {
			// An assistant turn made entirely of tool calls is represented by the
			// individual tool items below, not by an empty assistant item.
			if msg.Role != "assistant" || !hasToolCalls(msg.Content) {
				continue
			}
		}
		showMessage := strings.TrimSpace(text) != "" || omitted
		if msg.Role == "assistant" && strings.TrimSpace(text) == "" && onlyThinkingAndToolCalls(msg.Content) {
			showMessage = false
		}
		if showMessage {
			item := ConversationMessage{ID: id, Role: msg.Role, Text: text, Omitted: omitted, Truncated: truncated}
			if msg.Timestamp > 0 {
				item.Timestamp = time.Unix(msg.Timestamp, 0).UTC()
			}
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
			toolID := fmt.Sprintf("tool:%s:%d", id, blockIndex)
			toolItem := ConversationMessage{
				ID:        toolID,
				Role:      "tool",
				Tool:      block.ToolName,
				Summary:   conversationToolSummary(block.ToolName, block.Arguments),
				Status:    "pending",
				Timestamp: conversationTimestamp(msg.Timestamp),
			}
			if result, found := results[block.ToolCallID]; found {
				toolItem.Status = conversationToolStatus(result)
				if result.Timestamp > 0 {
					toolItem.Timestamp = conversationTimestamp(result.Timestamp)
				}
				details[toolID] = conversationToolDetailFromResult(result)
			} else {
				details[toolID] = conversationToolDetail{}
			}
			out = append(out, toolItem)
		}
	}
	return conversationProjection{messages: out, toolDetails: details}
}

func hasToolCalls(content []core.Content) bool {
	for _, block := range content {
		if block.Type == "tool_call" {
			return true
		}
	}
	return false
}

func onlyThinkingAndToolCalls(content []core.Content) bool {
	for _, block := range content {
		if block.Type != "thinking" && block.Type != "tool_call" && block.Type != "text" {
			return false
		}
	}
	return true
}

func conversationTimestamp(timestamp int64) time.Time {
	if timestamp == 0 {
		return time.Time{}
	}
	return time.Unix(timestamp, 0).UTC()
}

func conversationToolStatus(result core.AgentMessage) string {
	if result.Custom != nil && result.Custom["rejected"] == true {
		return "rejected"
	}
	if result.IsError {
		return "error"
	}
	return "ok"
}

func conversationToolDetailFromResult(result core.AgentMessage) conversationToolDetail {
	output, truncated := conversationToolResultTail(result.Content, maxConversationToolDetailBytes)
	return conversationToolDetail{output: output, truncated: truncated}
}

func conversationToolResultTail(content []core.Content, maxBytes int) (string, bool) {
	var output string
	truncated := false
	for _, block := range content {
		if block.Type != "text" {
			continue
		}
		part := strings.ToValidUTF8(block.Text, "�")
		if output != "" {
			part = "\n" + part
		}
		var wasTruncated bool
		output, wasTruncated = appendConversationTail(output, part, maxBytes)
		truncated = truncated || wasTruncated
	}
	return output, truncated
}

func appendConversationTail(current, part string, maxBytes int) (string, bool) {
	if maxBytes <= 0 {
		return "", current != "" || part != ""
	}
	if len(part) > maxBytes {
		return conversationTail(part, maxBytes), true
	}
	if len(current)+len(part) <= maxBytes {
		return current + part, false
	}
	return conversationTail(current+part, maxBytes), true
}

func conversationTail(value string, maxBytes int) string {
	start := len(value) - maxBytes
	for start < len(value) && !utf8.RuneStart(value[start]) {
		start++
	}
	return value[start:]
}

func conversationToolSummary(name string, args map[string]any) string {
	path := conversationToolArg(args, "path", "file", "filename")
	command := conversationToolArg(args, "command", "cmd")
	task := conversationToolArg(args, "task", "prompt", "message")
	var summary string
	switch name {
	case "read":
		summary = "leyó " + conversationToolTarget(path, "un archivo")
	case "edit":
		summary = "editó " + conversationToolTarget(path, "un archivo")
	case "write":
		summary = "escribió " + conversationToolTarget(path, "un archivo")
	case "apply_patch", "multiedit":
		summary = "aplicó cambios"
	case "bash":
		if command != "" {
			summary = "ejecutó `" + command + "`"
		} else {
			summary = "ejecutó un comando"
		}
	case "find":
		summary = "buscó archivos"
		if path != "" {
			summary += " en " + path
		}
	case "grep":
		summary = "buscó texto"
		if path != "" {
			summary += " en " + path
		}
	case "subagent":
		if task != "" {
			summary = "lanzó subagente: " + task
		} else {
			summary = "lanzó un subagente"
		}
	default:
		summary = "ejecutó " + name
	}
	return conversationSummaryLine(summary)
}

func conversationToolArg(args map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := args[key].(string); ok && value != "" {
			return value
		}
	}
	return ""
}

func conversationToolTarget(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func conversationSummaryLine(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	value = strings.ToValidUTF8(value, "�")
	if len(value) <= maxConversationToolSummaryBytes {
		return value
	}
	return conversationTail(value, maxConversationToolSummaryBytes-3) + "..."
}

func safeDisplayText(content []core.Content) (text string, omitted, truncated bool) {
	var b strings.Builder
	for _, block := range content {
		if block.Type == "tool_call" {
			continue
		}
		if block.Type != "text" {
			omitted = true
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		remaining := maxConversationTextBytes - b.Len()
		if remaining <= 0 {
			truncated = true
			break
		}
		part := block.Text
		if len(part) > remaining {
			part = part[:remaining]
			for len(part) > 0 && !utf8.ValidString(part) {
				part = part[:len(part)-1]
			}
			truncated = true
		}
		b.WriteString(part)
	}
	return b.String(), omitted, truncated
}

func (m *Manager) encodeConversationCursor(cursor conversationCursor) (string, error) {
	if len(m.conversationKey) == 0 {
		return "", fmt.Errorf("cursor key unavailable")
	}
	payload, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, m.conversationKey)
	_, _ = mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (m *Manager) decodeConversationCursor(raw string) (conversationCursor, error) {
	if len(m.conversationKey) == 0 {
		return conversationCursor{}, fmt.Errorf("cursor key unavailable")
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 2 || len(parts[0]) > 1024 || len(parts[1]) > 128 {
		return conversationCursor{}, fmt.Errorf("malformed cursor")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return conversationCursor{}, err
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return conversationCursor{}, err
	}
	mac := hmac.New(sha256.New, m.conversationKey)
	_, _ = mac.Write(payload)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return conversationCursor{}, fmt.Errorf("invalid cursor signature")
	}
	var cursor conversationCursor
	if err := json.Unmarshal(payload, &cursor); err != nil {
		return conversationCursor{}, err
	}
	return cursor, nil
}
