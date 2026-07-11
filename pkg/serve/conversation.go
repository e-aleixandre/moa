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
	defaultConversationPageSize = 50
	maxConversationPageSize     = 100
	maxConversationTextBytes    = 12 << 10
)

// ConversationMessage is the intentionally small owner-facing transcript DTO.
// It contains display text only; provider, tool, attachment and custom fields
// never cross this boundary.
type ConversationMessage struct {
	ID            string    `json:"id"`
	Role          string    `json:"role"`
	Timestamp     time.Time `json:"timestamp,omitempty"`
	Text          string    `json:"text"`
	Truncated     bool      `json:"truncated,omitempty"`
	Omitted       bool      `json:"omitted,omitempty"`
	OmittedBlocks int       `json:"omitted_blocks,omitempty"`
}

type conversationBranch struct {
	LeafID string `json:"leaf_id,omitempty"`
	Source string `json:"source"` // active or saved
}

type conversationResponse struct {
	SessionID  string                `json:"session_id"`
	Title      string                `json:"title"`
	Branch     conversationBranch    `json:"branch"`
	Messages   []ConversationMessage `json:"messages"`
	NextCursor string                `json:"next_cursor,omitempty"`
	HasMore    bool                  `json:"has_more"`
}

type conversationSnapshot struct {
	id       string
	title    string
	leafID   string
	source   string
	messages []ConversationMessage
}

type conversationCursor struct {
	SessionID string `json:"s"`
	Branch    string `json:"b"`
	Digest    string `json:"d"`
	Offset    int    `json:"o"`
}

func handleConversationMessages(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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
		digest := conversationDigest(snapshot)
		offset := 0
		if raw := r.URL.Query().Get("cursor"); raw != "" {
			cursor, err := m.decodeConversationCursor(raw)
			if err != nil || cursor.SessionID != snapshot.id || cursor.Branch != snapshot.leafID || cursor.Digest != digest || cursor.Offset < 0 || cursor.Offset > len(snapshot.messages) {
				http.Error(w, "invalid cursor", http.StatusBadRequest)
				return
			}
			offset = cursor.Offset
		}
		end := min(offset+limit, len(snapshot.messages))
		response := conversationResponse{
			SessionID: snapshot.id,
			Title:     snapshot.title,
			Branch:    conversationBranch{LeafID: snapshot.leafID, Source: snapshot.source},
			Messages:  snapshot.messages[offset:end],
			HasMore:   end < len(snapshot.messages),
		}
		if response.HasMore {
			response.NextCursor, err = m.encodeConversationCursor(conversationCursor{SessionID: snapshot.id, Branch: snapshot.leafID, Digest: digest, Offset: end})
			if err != nil {
				http.Error(w, "conversation unavailable", http.StatusServiceUnavailable)
				return
			}
		}
		writeJSON(w, http.StatusOK, response)
	}
}

func (m *Manager) conversationSnapshot(id string) (conversationSnapshot, error) {
	if sess, ok := m.Get(id); ok {
		msgs, err := bus.QueryTyped[bus.GetDisplayMessages, []core.AgentMessage](sess.runtime.Bus, bus.GetDisplayMessages{})
		if err != nil {
			return conversationSnapshot{}, err
		}
		return conversationSnapshot{id: sess.ID, title: sess.title(), leafID: sess.runtime.Context().Tree.LeafID(), source: "active", messages: safeConversationMessages(msgs)}, nil
	}

	// This intentionally does not call ResumeSession or session.FindSession:
	// both can initialize state and the latter can migrate legacy files.
	saved, _, err := session.FindSessionReadOnly(m.sessionBaseDir, id)
	if err != nil {
		return conversationSnapshot{}, err
	}
	msgs, leaf, err := savedConversationMessages(saved)
	if err != nil {
		return conversationSnapshot{}, err
	}
	return conversationSnapshot{id: saved.ID, title: saved.Title, leafID: leaf, source: "saved", messages: msgs}, nil
}

func savedConversationMessages(saved *session.Session) ([]ConversationMessage, string, error) {
	if len(saved.Entries) == 0 {
		return safeConversationMessages(saved.Messages), "", nil
	}
	tree, err := session.NewTreeFromEntries(saved.Entries, saved.LeafID)
	if err != nil {
		return nil, "", err
	}
	return safeConversationMessages(tree.AllMessages()), tree.LeafID(), nil
}

func safeConversationMessages(messages []core.AgentMessage) []ConversationMessage {
	out := make([]ConversationMessage, 0, len(messages))
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
		text, omitted, truncated := safeDisplayText(msg.Content)
		if strings.TrimSpace(text) == "" && !omitted {
			continue
		}
		item := ConversationMessage{ID: id, Role: msg.Role, Text: text, Omitted: omitted, Truncated: truncated}
		if msg.Timestamp > 0 {
			item.Timestamp = time.Unix(msg.Timestamp, 0).UTC()
		}
		for _, block := range msg.Content {
			if block.Type != "text" {
				item.OmittedBlocks++
			}
		}
		out = append(out, item)
	}
	return out
}

func safeDisplayText(content []core.Content) (text string, omitted, truncated bool) {
	var b strings.Builder
	for _, block := range content {
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

func conversationDigest(snapshot conversationSnapshot) string {
	h := sha256.New()
	_, _ = h.Write([]byte(snapshot.id))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(snapshot.leafID))
	for _, message := range snapshot.messages {
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(message.ID))
	}
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil)[:12])
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
