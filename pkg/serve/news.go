package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/push"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

// NewsItem is the API form of a tasks.News: something a session deliberately
// told the user (For you › News). It is written on purpose with the notify
// tool, never derived from every message; its read state lives in the task
// database so a second device does not show it again.
type NewsItem struct {
	ID        string     `json:"id"`
	SessionID string     `json:"session_id"`
	Key       string     `json:"key,omitempty"`
	Kind      string     `json:"kind"` // done | deployed | problem | info
	Title     string     `json:"title"`
	Body      string     `json:"body,omitempty"`
	Revision  int64      `json:"revision"`
	Created   time.Time  `json:"created"`
	Updated   time.Time  `json:"updated"`
	ReadAt    *time.Time `json:"read_at,omitempty"`
}

func newsItem(n tasks.News) NewsItem {
	it := NewsItem{
		ID: n.ID, SessionID: n.SessionID, Key: n.Key, Kind: n.Kind, Title: n.Title, Body: n.Body,
		Revision: n.Revision, Created: time.UnixMilli(n.CreatedAt).UTC(), Updated: time.UnixMilli(n.UpdatedAt).UTC(),
	}
	if n.ReadAt != 0 {
		t := time.UnixMilli(n.ReadAt).UTC()
		it.ReadAt = &t
	}
	return it
}

// listNews is the current news, unread and read within tasks.NewsKeep.
func (m *Manager) listNews(ctx context.Context) ([]NewsItem, error) {
	out := []NewsItem{}
	if m.tasks == nil {
		return out, nil
	}
	list, err := m.tasks.ListNews(ctx)
	if err != nil {
		return nil, err
	}
	for _, n := range list {
		out = append(out, newsItem(n))
	}
	return out, nil
}

// publishNews commits an item and only then announces a problem: a push
// for something the database does not hold would point at nothing. A retry
// of the same invocation does not announce it again.
func (m *Manager) publishNews(ctx context.Context, in tasks.NewsInput) (tasks.NewsPublished, error) {
	if m.tasks == nil {
		return tasks.NewsPublished{}, fmt.Errorf("%w: no task database", tasks.ErrUnavailable)
	}
	p, err := m.tasks.PublishNews(ctx, in)
	if err != nil {
		return tasks.NewsPublished{}, err
	}
	if p.Kind == tasks.NewsProblem && p.Outcome != tasks.NewsDuplicate && m.pushPolicy != nil {
		// Per the push contract the lock screen shows the kind and the
		// session title, never the item's own title or body.
		s := push.Signal{Kind: push.KindProblem, SessionID: p.SessionID}
		if sess, ok := m.Get(p.SessionID); ok {
			s.Title = sess.title()
		}
		m.pushPolicy.Handle(s)
	}
	return p, nil
}

// notifyAllowed is who publishes News: an owner's own conversation and a
// session in a codebase without an owner. A session working under an owner
// reports to it instead, and its owner decides what reaches the user.
func notifyAllowed(ownerSession, hasProjectOwner bool) bool {
	return ownerSession || !hasProjectOwner
}

const notifyDescription = `Tell the user something worth keeping, outside this conversation: it lands in their For you › News, unread until they read it, and it survives restarts. Use it on purpose, for what a project lead would report: something finished that matters, a deploy, a problem, or a short digest of several changes. Not for every step, and never for a question (use ask_user). Title: the fact in one line ("Webhook retries fix deployed to production"). Body: 2-4 short lines of what happened and why it matters. Pass a key to update the same item later instead of adding another (e.g. "deploy/webhook-retries": "in preflight", then "deployed"). Kind "problem" also sends a notification to the user's phone; use it only for something they need to act on. When the user later asks about something you already notified, say so and point to it ("I left it in For you at 10:40: ...") instead of reporting it as new. Action "list" shows what this session already notified and whether the user read it.`

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
		Execute: func(ctx context.Context, params map[string]any, _ func(core.Result)) (core.Result, error) {
			if action, _ := params["action"].(string); action == "list" {
				list, err := m.listNews(ctx)
				if err != nil {
					return core.ErrorResult("could not read the news: " + err.Error()), nil
				}
				var b strings.Builder
				for _, it := range list {
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
			if len(title) > 160 || strings.ContainsAny(title, "\r\n") {
				return core.ErrorResult("title too long: one line, max 120 characters"), nil
			}
			body, _ := params["body"].(string)
			if len(body) > 1200 {
				return core.ErrorResult("body too long: 2-4 short lines"), nil
			}
			kind, _ := params["kind"].(string)
			switch kind {
			case tasks.NewsDone, tasks.NewsDeployed, tasks.NewsProblem, tasks.NewsInfo:
			default:
				kind = tasks.NewsInfo
			}
			key, _ := params["key"].(string)
			origin := ""
			if callID := core.ToolCallIDFromContext(ctx); callID != "" {
				origin = core.ToolCallMessageIDFromContext(ctx) + "/" + callID
			}
			p, err := m.publishNews(ctx, tasks.NewsInput{SessionID: sessionID, Key: key, Kind: kind, Title: title, Body: body, Origin: origin})
			if err != nil {
				return core.ErrorResult("could not save the news: " + err.Error()), nil
			}
			verb := "Published"
			if p.Outcome == tasks.NewsUpdated {
				verb = "Updated"
			}
			return core.TextResult(fmt.Sprintf("%s in the user's For you as %s. If they ask about it later, point to it instead of repeating it as new.", verb, p.ID)), nil
		},
	}
}

// handleNewsRead serves POST /api/for-you/news/read
// {"items":[{"id":"news_…","revision":2}]}: each item is marked read at the
// revision the client showed. "Mark all read" sends every item it showed; an
// empty list is refused.
func handleNewsRead(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Items []struct {
				ID       string `json:"id"`
				Revision int64  `json:"revision"`
			} `json:"items"`
		}
		if !decodeStrict(w, r, &body) {
			return
		}
		if m.tasks == nil {
			writeTaskError(w, tasks.ErrUnavailable)
			return
		}
		acks := make([]tasks.NewsAck, 0, len(body.Items))
		for _, it := range body.Items {
			acks = append(acks, tasks.NewsAck{ID: it.ID, Revision: it.Revision})
		}
		n, err := m.tasks.ReadNews(r.Context(), acks)
		if err != nil {
			if !errors.Is(err, tasks.ErrInvalid) {
				slog.Warn("news: marking read failed", "error", err)
			}
			writeTaskError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]int{"marked": n})
	}
}

// The recent-news block bounds: what the model reads to avoid reporting
// something as new, never a report itself.
const (
	recentNewsMaxItems = 10
	recentNewsMaxText  = 200
	recentNewsMaxBytes = 2048
	recentNewsWindow   = tasks.NewsKeep
)

// recentNewsBlock lists what this session already notified in the last week,
// newest first, with whether the user read it. "" when there is nothing (or
// it cannot be read: the block is advice, never a reason to fail a request).
func (m *Manager) recentNewsBlock(ctx context.Context, sessionID string) string {
	if m.tasks == nil {
		return ""
	}
	list, err := m.tasks.ListNews(ctx)
	if err != nil {
		return ""
	}
	cutoff := m.clock.Now().Add(-recentNewsWindow).UnixMilli()
	var mine []tasks.News
	for _, n := range list {
		if n.SessionID == sessionID && n.UpdatedAt >= cutoff {
			mine = append(mine, n)
		}
	}
	if len(mine) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<recent_news>\nWhat you already told the user in For you › News. When they ask about one of these, point to it instead of reporting it as new; use notify with a key to update it.\n")
	const closing = "</recent_news>"
	const more = "(and 99999 more)\n"
	for i, n := range mine {
		read := "unread"
		if n.ReadAt != 0 {
			read = "read"
		}
		line := fmt.Sprintf("- %s [%s, %s, %s]: %s\n", n.ID, n.Kind, time.UnixMilli(n.UpdatedAt).UTC().Format(time.RFC3339), read,
			escapedPrefix(n.Title, recentNewsMaxText))
		if i == recentNewsMaxItems || b.Len()+len(line)+len(more)+len(closing) > recentNewsMaxBytes {
			fmt.Fprintf(&b, "(and %d more)\n", len(mine)-i)
			break
		}
		b.WriteString(line)
	}
	b.WriteString(closing)
	return b.String()
}

// composeEphemeral joins the per-request blocks into the one message the
// agent appends: the open questions first, exactly as they were, then the
// news. Without news it is the questions message itself.
func composeEphemeral(questions func() *core.Message, news func() string) func() *core.Message {
	return func() *core.Message {
		var q *core.Message
		if questions != nil {
			q = questions()
		}
		n := news()
		if n == "" {
			return q
		}
		if q == nil {
			msg := core.NewUserMessage(n)
			return &msg
		}
		msg := *q
		msg.Content = append([]core.Content(nil), q.Content...)
		last := len(msg.Content) - 1
		if last >= 0 && msg.Content[last].Type == "text" {
			msg.Content[last].Text += "\n" + n
		} else {
			msg.Content = append(msg.Content, core.TextContent(n))
		}
		return &msg
	}
}

// ephemeralContext is the session's per-request block: the open-questions
// inventory and, for a session that publishes News, its recent news.
func (m *Manager) ephemeralContext(askAsync bool, sessionID string, notifyOn *bool, session func() *ManagedSession) func() *core.Message {
	questions := m.questions.inventory(askAsync, session)
	return composeEphemeral(questions, func() string {
		if !*notifyOn {
			return ""
		}
		return m.readNews(func() string {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			return m.recentNewsBlock(ctx, sessionID)
		})
	})
}

// readNews runs a per-request News read while the Manager is open. Once
// Shutdown has closed the gate it returns "" without touching storage: the
// block is advice, never a reason to fail a request.
func (m *Manager) readNews(read func() string) string {
	m.newsGate.RLock()
	defer m.newsGate.RUnlock()
	if m.newsClosed {
		return ""
	}
	return read()
}
