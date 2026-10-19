package tasks

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"strings"
	"time"
)

// News is what a session deliberately told the user (For you › News): done,
// deployed, a problem. Its read state lives here, not on a device, so a second
// device does not show it again.
//
// Invariants: an item is identified by its ID forever; a session's non-empty
// key names one item, and publishing with it again updates that item in place
// (same ID, same created_at), bumps its revision and makes it unread. Reading
// acknowledges one revision: a device that saw an older one cannot archive the
// update it never showed. Unread items never expire; a read one is listed for
// NewsKeep after it was first read, then dropped.

// NewsKeep is how long a read item stays listed (For you › Earlier).
const NewsKeep = 7 * 24 * time.Hour

// News kinds.
const (
	NewsDone     = "done"
	NewsDeployed = "deployed"
	NewsProblem  = "problem"
	NewsInfo     = "info"
)

// News is one item. Times are Unix milliseconds by the repository's clock;
// ReadAt is 0 while unread.
type News struct {
	ID        string
	SessionID string
	Key       string
	Kind      string
	Title     string
	Body      string
	Revision  int64
	CreatedAt int64
	UpdatedAt int64
	ReadAt    int64
}

// NewsInput is a publish. Origin identifies the invocation (message and tool
// call) that published it: the same invocation retried is the same item, not
// an update.
type NewsInput struct {
	SessionID string
	Key       string
	Kind      string
	Title     string
	Body      string
	Origin    string
}

// NewsOutcome says what a publish did.
type NewsOutcome string

const (
	NewsCreated   NewsOutcome = "created"
	NewsUpdated   NewsOutcome = "updated"
	NewsDuplicate NewsOutcome = "duplicate" // a retry of the invocation that already published it
)

// NewsPublished is the item after a publish and what the publish did.
type NewsPublished struct {
	News
	Outcome NewsOutcome
}

// NewsAck acknowledges one revision of an item as read.
type NewsAck struct {
	ID       string
	Revision int64
}

const newsCols = `id, session_id, key, kind, title, body, revision, created_at, updated_at, read_at`

func scanNews(sc interface{ Scan(...any) error }) (News, error) {
	var n News
	var read sql.NullInt64
	if err := sc.Scan(&n.ID, &n.SessionID, &n.Key, &n.Kind, &n.Title, &n.Body, &n.Revision, &n.CreatedAt, &n.UpdatedAt, &read); err != nil {
		return News{}, err
	}
	n.ReadAt = read.Int64
	return n, nil
}

func queryOneNews(ctx context.Context, q querier, where string, args ...any) (News, bool, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+newsCols+" FROM news WHERE "+where+" LIMIT 1", args...)
	if err != nil {
		return News{}, false, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		return News{}, false, rows.Err()
	}
	n, err := scanNews(rows)
	return n, err == nil, err
}

func newNewsID() string {
	var b [10]byte
	_, _ = rand.Read(b[:])
	return "news_" + hex.EncodeToString(b[:])
}

// PublishNews adds an item or, with a key the session already used, updates
// it. One transaction: two writers with the same key never make two items.
func (r *Repo) PublishNews(ctx context.Context, in NewsInput) (NewsPublished, error) {
	in.Title = strings.TrimSpace(in.Title)
	in.Body = strings.TrimSpace(in.Body)
	in.Key = strings.TrimSpace(in.Key)
	switch {
	case in.SessionID == "":
		return NewsPublished{}, invalid("news needs a session")
	case in.Title == "":
		return NewsPublished{}, invalid("news needs a title")
	}
	switch in.Kind {
	case NewsDone, NewsDeployed, NewsProblem, NewsInfo:
	default:
		return NewsPublished{}, invalid("unknown news kind %q", in.Kind)
	}
	var out NewsPublished
	err := r.write(ctx, func(tx *sql.Tx) (bool, error) {
		now := r.now().UnixMilli()
		if _, err := tx.ExecContext(ctx, `DELETE FROM news WHERE read_at IS NOT NULL AND read_at < ?`,
			now-NewsKeep.Milliseconds()); err != nil {
			return false, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM news_origins WHERE news_id NOT IN (SELECT id FROM news)`); err != nil {
			return false, err
		}
		if in.Origin != "" {
			// Any invocation that ever wrote an item, not only its latest:
			// a retry of an older one must not undo a later update.
			n, found, err := queryOneNews(ctx, tx, "id = (SELECT news_id FROM news_origins WHERE session_id = ? AND origin = ?)", in.SessionID, in.Origin)
			if err != nil {
				return false, err
			}
			if found {
				out = NewsPublished{News: n, Outcome: NewsDuplicate}
				return false, nil
			}
		}
		if in.Key != "" {
			n, found, err := queryOneNews(ctx, tx, "session_id = ? AND key = ?", in.SessionID, in.Key)
			if err != nil {
				return false, err
			}
			if found {
				if _, err := tx.ExecContext(ctx, `UPDATE news SET kind = ?, title = ?, body = ?, origin = ?,
					revision = revision + 1, updated_at = ?, read_at = NULL WHERE id = ?`,
					in.Kind, in.Title, in.Body, in.Origin, now, n.ID); err != nil {
					return false, err
				}
				n.Kind, n.Title, n.Body, n.UpdatedAt, n.ReadAt = in.Kind, in.Title, in.Body, now, 0
				n.Revision++
				if err := recordNewsOrigin(ctx, tx, in, n.ID); err != nil {
					return false, err
				}
				out = NewsPublished{News: n, Outcome: NewsUpdated}
				return true, nil
			}
		}
		n := News{ID: newNewsID(), SessionID: in.SessionID, Key: in.Key, Kind: in.Kind, Title: in.Title, Body: in.Body,
			Revision: 1, CreatedAt: now, UpdatedAt: now}
		if _, err := tx.ExecContext(ctx, `INSERT INTO news(id, session_id, key, origin, kind, title, body, revision, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,1,?,?)`, n.ID, n.SessionID, n.Key, in.Origin, n.Kind, n.Title, n.Body, now, now); err != nil {
			return false, err
		}
		if err := recordNewsOrigin(ctx, tx, in, n.ID); err != nil {
			return false, err
		}
		out = NewsPublished{News: n, Outcome: NewsCreated}
		return true, nil
	})
	if err != nil {
		return NewsPublished{}, err
	}
	return out, nil
}

// recordNewsOrigin remembers that in's invocation wrote item id.
func recordNewsOrigin(ctx context.Context, tx *sql.Tx, in NewsInput, id string) error {
	if in.Origin == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO news_origins(session_id, origin, news_id) VALUES (?,?,?)`,
		in.SessionID, in.Origin, id)
	return err
}

// ListNews returns the unread items and those read less than NewsKeep ago,
// most recently updated first. A database that does not exist yet has none.
func (r *Repo) ListNews(ctx context.Context) ([]News, error) {
	rd, err := r.reader()
	if err != nil || rd == nil {
		return nil, err
	}
	rows, err := rd.QueryContext(ctx, "SELECT "+newsCols+` FROM news
		WHERE read_at IS NULL OR read_at >= ? ORDER BY updated_at DESC, rowid DESC`,
		r.now().UnixMilli()-NewsKeep.Milliseconds())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []News
	for rows.Next() {
		n, err := scanNews(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// ReadNews marks items read, each only at the revision acknowledged and only
// the first time: acknowledging again does not move read_at. Unknown items
// and stale revisions are skipped. It returns how many it marked. An empty
// list, or an empty ID, is an error: "mark all" is the client's snapshot of
// what it showed, never a blanket write.
func (r *Repo) ReadNews(ctx context.Context, acks []NewsAck) (int, error) {
	if len(acks) == 0 {
		return 0, invalid("no news to mark read")
	}
	for _, a := range acks {
		if a.ID == "" {
			return 0, invalid("news id is required")
		}
	}
	var marked int
	err := r.write(ctx, func(tx *sql.Tx) (bool, error) {
		marked = 0
		now := r.now().UnixMilli()
		for _, a := range acks {
			res, err := tx.ExecContext(ctx, `UPDATE news SET read_at = ? WHERE id = ? AND revision = ? AND read_at IS NULL`,
				now, a.ID, a.Revision)
			if err != nil {
				return false, err
			}
			n, _ := res.RowsAffected()
			marked += int(n)
		}
		return marked > 0, nil
	})
	if err != nil {
		return 0, err
	}
	return marked, nil
}
