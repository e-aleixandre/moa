package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
)

// ReadSessionToolName is the tool with which a normal conversation reads the
// sessions its human explicitly selected (@) in it.
const ReadSessionToolName = "read_session"

const (
	defaultMentionSessionsLimit = 5
	maxMentionSessionsLimit     = 50
)

var errMentionsShape = errors.New("session_mentions must be an array of full session IDs")

// parseSessionMentions validates the optional session_mentions field of a
// human /send. Absent, null and [] mean "no receipt". Anything malformed is an
// error: the request is rejected whole, never granted partially. Duplicates
// collapse by exact equality, keeping first-seen order.
func parseSessionMentions(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var items []any
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, errMentionsShape
	}
	var out []string
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		id, ok := item.(string)
		if !ok {
			return nil, errMentionsShape
		}
		if err := session.ValidateID(id); err != nil {
			return nil, fmt.Errorf("session_mentions: %w", err)
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out, nil
}

// trustedSessionMentions returns the session IDs a user message's receipt
// grants. The receipt is strict: only a user message, only the receipt key
// (plus the compaction epoch stamp), only a non-empty list of valid IDs, as
// []string in memory or []any after a copy or JSON round trip. Anything else
// invalidates the whole receipt.
func trustedSessionMentions(msg *core.AgentMessage) []string {
	if msg.Role != "user" || len(msg.Custom) == 0 {
		return nil
	}
	var raw any
	for key, value := range msg.Custom {
		switch key {
		case core.CustomKeyHumanSessionMentions:
			raw = value
		case "compaction_epoch":
		default:
			return nil
		}
	}
	var ids []string
	switch list := raw.(type) {
	case []string:
		ids = list
	case []any:
		for _, item := range list {
			id, ok := item.(string)
			if !ok {
				return nil
			}
			ids = append(ids, id)
		}
	default:
		return nil
	}
	if len(ids) == 0 {
		return nil
	}
	for _, id := range ids {
		if session.ValidateID(id) != nil {
			return nil
		}
	}
	return append([]string(nil), ids...)
}

// mentionedSessions is the union of valid receipts over the caller's active
// branch, in-flight messages included. IDs are copied out inside the visit so
// nothing is resolved or read while the transcript lock is held.
func mentionedSessions(caller *ManagedSession) map[string]struct{} {
	out := make(map[string]struct{})
	caller.runtime.Context().VisitTranscript(func(msg *core.AgentMessage) {
		for _, id := range trustedSessionMentions(msg) {
			out[id] = struct{}{}
		}
	})
	return out
}

// sameCodebase reports whether two non-empty directories share a CodebaseKey.
func sameCodebase(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return codebaseKeyOf(a) == codebaseKeyOf(b)
}

// newReadSessionTool builds the read-only handle with which a normal
// conversation reads a session its human selected with @. The caller is fixed
// at construction by the server: the model supplies neither identity nor scope.
//
// Authorization is the union of receipts (human_session_mentions, minted only by
// the human /send path) on the caller's active branch, and the target is
// revalidated on the very object that is read, every time. Detached targets
// are readable from a normal session; owners get no access from this tool.
func newReadSessionTool(mgr *Manager, callerID string, caller func() *ManagedSession) core.Tool {
	return core.Tool{
		Name:  ReadSessionToolName,
		Label: "Read session",
		Description: "Read another session of this project that the user mentioned with @ in this " +
			"conversation. Returns its last messages, each abridged to its beginning and end with " +
			"a notice of what was cut; message_id reads one message whole (offset pages a very long " +
			"one), and tools=true adds the tool calls. Only sessions the user mentioned here can be read.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"session_id": {"type": "string", "description": "The full ID of a session the user mentioned."},
				"limit": {"type": "integer", "description": "How many recent messages (default 20, max 60)."},
				"message_id": {"type": "string", "description": "Read this one message (or tool call) whole instead of the abridged list. \"last\" is the session's latest assistant message."},
				"offset": {"type": "integer", "description": "With message_id: character to start from, for a message longer than one read returns."},
				"tools": {"type": "boolean", "description": "Also list the tool calls, with abridged arguments and results (default false)."}
			},
			"required": ["session_id"]
		}`),
		Effect: core.EffectReadOnly,
		Execute: func(ctx context.Context, params map[string]any, _ func(core.Result)) (core.Result, error) {
			id, _ := params["session_id"].(string)
			if id == "" {
				return core.ErrorResult("session_id is required"), nil
			}
			self := caller()
			if self == nil || self.runtime == nil || self.runtime.Context() == nil || self.ID != callerID {
				return core.ErrorResult("this session's history is not available"), nil
			}
			if _, ok := mentionedSessions(self)[id]; !ok {
				return core.ErrorResult("you can only read sessions the user mentioned (@) in this conversation"), nil
			}
			if id == callerID {
				return core.ErrorResult("that is your own conversation"), nil
			}
			self.mu.Lock()
			callerCWD := self.CWD
			self.mu.Unlock()
			snapshot, err := mgr.conversationSnapshotFor(id, func(target readTarget) error {
				if target.kind == session.KindOwner || !sameCodebase(callerCWD, target.cwd) {
					return fmt.Errorf("session %s is not in this conversation's project", id)
				}
				return nil
			})
			if err != nil {
				if errors.Is(err, session.ErrNotFound) {
					return core.ErrorResult(fmt.Sprintf("session %s not found", id)), nil
				}
				return core.ErrorResult(fmt.Sprintf("cannot read session %s: %v", id, err)), nil
			}
			return readSessionSnapshot(snapshot, sessionReadOptions{
				limit:     getNum(params, "limit"),
				messageID: getStr(params, "message_id"),
				offset:    getNum(params, "offset"),
				tools:     params["tools"] == true,
			}), nil
		},
	}
}

// mentionSessionRow is one candidate of the composer's @ menu.
type mentionSessionRow struct {
	ID      string       `json:"id"`
	Title   string       `json:"title"`
	State   SessionState `json:"state"`
	Updated time.Time    `json:"updated"`
	Saved   bool         `json:"saved"`
}

// handleMentionSessions lists the sessions an @ in this conversation can
// offer: same codebase, live and saved, newest first, never an owner or the
// conversation itself. An owner's conversation does not see detached
// sessions, which it cannot read.
func handleMentionSessions(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		self, ok := mgr.Get(r.PathValue("id"))
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		limit := defaultMentionSessionsLimit
		if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l > 0 {
			limit = min(l, maxMentionSessionsLimit)
		}
		query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
		self.mu.Lock()
		selfCWD, selfKind := self.CWD, self.Kind
		self.mu.Unlock()

		rows := []mentionSessionRow{}
		if selfCWD == "" {
			writeJSON(w, http.StatusOK, rows)
			return
		}
		selfKey := codebaseKeyOf(selfCWD)
		keys := map[string]string{selfCWD: selfKey}
		sameKey := func(cwd string) bool {
			key, known := keys[cwd]
			if !known {
				key = codebaseKeyOf(cwd)
				keys[cwd] = key
			}
			return key == selfKey
		}
		// The roster (its saved part is cached) only enumerates candidates; a
		// saved one is offered from its current record, loaded read-only, so a
		// session that moved codebase or was deleted since the scan is omitted.
		infos := mgr.ListWith(ListOptions{})
		sort.SliceStable(infos, func(i, j int) bool { return infos[i].Updated.After(infos[j].Updated) })
		for _, info := range infos {
			if len(rows) >= limit {
				break
			}
			if info.ID == self.ID || info.Kind == session.KindOwner || info.CWD == "" {
				continue
			}
			if selfKind == session.KindOwner && info.ownerDetached {
				continue
			}
			if query != "" && !strings.Contains(strings.ToLower(info.Title), query) {
				continue
			}
			if !sameKey(info.CWD) {
				continue
			}
			if info.State == StateSaved {
				current, _, err := session.FindSessionReadOnly(mgr.sessionBaseDir, info.ID)
				if err != nil {
					continue
				}
				cwd, _ := current.Metadata[session.MetaCWD].(string)
				if current.Kind() == session.KindOwner || cwd == "" || !sameKey(cwd) ||
					(selfKind == session.KindOwner && current.OwnerDetached()) ||
					(query != "" && !strings.Contains(strings.ToLower(current.Title), query)) {
					continue
				}
				info.Title, info.Updated = current.Title, current.Updated
			}
			rows = append(rows, mentionSessionRow{
				ID: info.ID, Title: info.Title, State: info.State,
				Updated: info.Updated, Saved: info.State == StateSaved,
			})
		}
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].Updated.After(rows[j].Updated) })
		if len(rows) > limit {
			rows = rows[:limit]
		}
		writeJSON(w, http.StatusOK, rows)
	}
}
