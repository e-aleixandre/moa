package serve

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// NewsItem is something a session deliberately told the user (For you ›
// News): done, deployed, a problem. It is written on purpose with the notify
// tool, never derived from every message, and its read state lives on the
// server so a second device does not show it again.
type NewsItem struct {
	ID        string     `json:"id"`
	SessionID string     `json:"session_id"`
	Key       string     `json:"key,omitempty"`
	Kind      string     `json:"kind"` // done | deployed | problem | info
	Title     string     `json:"title"`
	Body      string     `json:"body,omitempty"`
	Created   time.Time  `json:"created"`
	Updated   time.Time  `json:"updated"`
	ReadAt    *time.Time `json:"read_at,omitempty"`
}

// newsKeep is how long a read item stays listed under Earlier.
const newsKeep = 7 * 24 * time.Hour

// newsStore keeps the news in one JSON file beside the session store.
// PROTOTYPE: a file rather than a tasks.sqlite table, so the schema version
// does not move; where it lives for real is a persistence decision for review.
type newsStore struct {
	mu     sync.Mutex
	path   string
	items  []NewsItem
	loaded bool
}

func (s *newsStore) loadLocked() {
	if s.loaded {
		return
	}
	s.loaded = true
	if b, err := os.ReadFile(s.path); err == nil {
		_ = json.Unmarshal(b, &s.items)
	}
}

func (s *newsStore) saveLocked() error {
	b, err := json.MarshalIndent(s.items, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Publish adds an item or, with a key the session already used, updates it
// and makes it unread again ("in preflight" then "deployed" is one item).
func (s *newsStore) Publish(it NewsItem, now time.Time) (NewsItem, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	if it.Key != "" {
		for i := range s.items {
			old := &s.items[i]
			if old.SessionID == it.SessionID && old.Key == it.Key {
				old.Kind, old.Title, old.Body, old.Updated, old.ReadAt = it.Kind, it.Title, it.Body, now, nil
				return *old, true, s.saveLocked()
			}
		}
	}
	var b [8]byte
	_, _ = rand.Read(b[:])
	it.ID = "news_" + hex.EncodeToString(b[:])
	it.Created, it.Updated = now, now
	s.items = append(s.items, it)
	return it, false, s.saveLocked()
}

// List returns the unread items and the read ones younger than newsKeep,
// newest first; older read items are dropped from the file.
func (s *newsStore) List(now time.Time) []NewsItem {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	kept := make([]NewsItem, 0, len(s.items))
	for _, it := range s.items {
		if it.ReadAt != nil && now.Sub(*it.ReadAt) > newsKeep {
			continue
		}
		kept = append(kept, it)
	}
	if len(kept) != len(s.items) {
		s.items = kept
		_ = s.saveLocked()
	}
	out := append([]NewsItem(nil), kept...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out
}

// MarkRead archives items (read is archived); no ids marks every item.
func (s *newsStore) MarkRead(ids []string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	changed := false
	for i := range s.items {
		if (len(ids) == 0 || want[s.items[i].ID]) && s.items[i].ReadAt == nil {
			t := now
			s.items[i].ReadAt = &t
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return s.saveLocked()
}

func (m *Manager) newsStore() *newsStore {
	m.newsOnce.Do(func() {
		m.news = &newsStore{path: filepath.Join(filepath.Dir(m.sessionBaseDir), "news.json")}
	})
	return m.news
}

const notifyDescription = `Tell the user something worth keeping, outside this conversation: it lands in their For you › News, unread until they read it, and it survives restarts. Use it on purpose, for what a project lead would report: something finished that matters, a deploy, a problem, or a short digest of several changes. Not for every step, and never for a question (use ask_user). Title: the fact in one line ("Webhook retries fix deployed to production"). Body: 2-4 short lines of what happened and why it matters. Pass a key to update the same item later instead of adding another (e.g. "deploy/webhook-retries": "in preflight", then "deployed"). When the user later asks about something you already notified, say so and point to it ("I left it in For you at 10:40: ...") instead of reporting it as new. Action "list" shows what this session already notified and whether the user read it.`

func (m *Manager) newNotifyTool(sessionID string) core.Tool {
	return core.Tool{
		Name:        "notify",
		Label:       "Notify",
		Description: notifyDescription,
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"action": {"type": "string", "enum": ["post", "list"], "description": "Optional, default post."},
				"kind": {"type": "string", "enum": ["done", "deployed", "problem", "info"], "description": "Only with post: what kind of news it is."},
				"title": {"type": "string", "description": "Only with post: the fact, in one line (max 120 characters)."},
				"body": {"type": "string", "description": "Only with post: 2-4 short lines, what happened and why it matters to the user."},
				"key": {"type": "string", "description": "Optional: reuse it to update the same item instead of adding another."}
			}
		}`),
		Effect: core.EffectReadOnly,
		Execute: func(_ context.Context, params map[string]any, _ func(core.Result)) (core.Result, error) {
			store := m.newsStore()
			now := m.clock.Now()
			if action, _ := params["action"].(string); action == "list" {
				var b strings.Builder
				for _, it := range store.List(now) {
					if it.SessionID != sessionID {
						continue
					}
					read := "unread"
					if it.ReadAt != nil {
						read = "read " + it.ReadAt.Format(time.RFC3339)
					}
					fmt.Fprintf(&b, "- %s [%s, %s, %s]: %s\n", it.ID, it.Kind, it.Updated.Format(time.RFC3339), read, it.Title)
				}
				if b.Len() == 0 {
					return core.TextResult("This session has not notified anything yet."), nil
				}
				return core.TextResult(b.String()), nil
			}
			title, _ := params["title"].(string)
			title = strings.TrimSpace(title)
			if title == "" {
				return core.ErrorResult("title is required"), nil
			}
			if len(title) > 160 {
				return core.ErrorResult("title too long: one line, max 120 characters"), nil
			}
			body, _ := params["body"].(string)
			if len(body) > 1200 {
				return core.ErrorResult("body too long: 2-4 short lines"), nil
			}
			kind, _ := params["kind"].(string)
			switch kind {
			case "done", "deployed", "problem", "info":
			default:
				kind = "info"
			}
			key, _ := params["key"].(string)
			it, updated, err := store.Publish(NewsItem{SessionID: sessionID, Key: key, Kind: kind, Title: title, Body: strings.TrimSpace(body)}, now)
			if err != nil {
				return core.ErrorResult("could not save the news: " + err.Error()), nil
			}
			verb := "Published"
			if updated {
				verb = "Updated"
			}
			return core.TextResult(fmt.Sprintf("%s in the user's For you as %s. If they ask about it later, point to it instead of repeating it as new.", verb, it.ID)), nil
		},
	}
}

// handleNewsRead serves POST /api/for-you/news/read {"ids":[...]}; an empty
// list marks everything read.
func handleNewsRead(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			IDs []string `json:"ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		if err := m.newsStore().MarkRead(body.IDs, m.clock.Now()); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
