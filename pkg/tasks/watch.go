package tasks

import (
	"context"
	"database/sql"
	"time"
)

// Watch reports changes made by any process, including this one, until ctx
// ends: fn gets the new global revision. It compares PRAGMA data_version on
// one persistent reader connection, which SQLite bumps whenever a different
// connection commits — so a CLI write is noticed without WAL or filesystem
// watchers, and a local write (a different connection from this reader) is
// noticed too. A local commit also wakes the loop early instead of waiting
// for the next tick. Before the database exists it only polls for the file.
func (r *Repo) Watch(ctx context.Context, interval time.Duration, fn func(revision int64)) {
	var conn *sql.Conn
	var last int64
	waitedForFile := false
	defer func() {
		if conn != nil {
			_ = conn.Close()
		}
	}()
	version := func() (int64, error) {
		var v int64
		err := conn.QueryRowContext(ctx, "PRAGMA data_version").Scan(&v)
		return v, err
	}
	check := func() {
		if conn == nil {
			rd, err := r.reader()
			if err != nil {
				return
			}
			if rd == nil {
				waitedForFile = true
				return
			}
			c, err := rd.Conn(ctx)
			if err != nil {
				return
			}
			conn = c
			v, err := version()
			if err != nil {
				_ = conn.Close()
				conn = nil
				return
			}
			last = v
			if waitedForFile {
				waitedForFile = false
				rev, _ := r.Revision(ctx)
				fn(rev)
			}
			return
		}
		v, err := version()
		if err != nil {
			_ = conn.Close()
			conn = nil
			return
		}
		if v == last {
			return
		}
		last = v
		if rev, err := r.Revision(ctx); err == nil {
			fn(rev)
		}
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	check()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-r.changed:
		}
		check()
	}
}
