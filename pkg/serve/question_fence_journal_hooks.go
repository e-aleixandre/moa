package serve

import "sync/atomic"

type reviewLoadCallback struct {
	call func(*questionService, []string)
}

var reviewLoadAfterRead atomic.Pointer[reviewLoadCallback]

type reviewWriteCallback struct {
	call func(*questionService, string, string)
}

var reviewWriteAfterCreate atomic.Pointer[reviewWriteCallback]

func reviewObserveWrite(q *questionService, sessionID, path string) {
	if h := reviewWriteAfterCreate.Load(); h != nil {
		h.call(q, sessionID, path)
	}
}

type reviewSyncFault struct {
	path string
	err  error
}

var reviewSyncFailure atomic.Pointer[reviewSyncFault]

func reviewObserveLoad(q *questionService, ids []string) {
	if h := reviewLoadAfterRead.Load(); h != nil {
		h.call(q, ids)
	}
}
func reviewSyncJournalDir(path string) error {
	if f := reviewSyncFailure.Load(); f != nil && path == f.path {
		return f.err
	}
	return syncJournalDir(path)
}
