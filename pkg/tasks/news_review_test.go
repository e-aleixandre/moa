package tasks

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

func TestReviewNewsConcurrentPublishing(t *testing.T) {
	for _, keyed := range []bool{false, true} {
		t.Run(fmt.Sprintf("keyed=%v", keyed), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), DatabaseName)
			a, b := openRepo(t, path), openRepo(t, path)
			const writers = 24
			start := make(chan struct{})
			errCh := make(chan error, writers)
			var wg sync.WaitGroup
			for i := range writers {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					r := a
					if i%2 != 0 {
						r = b
					}
					in := NewsInput{SessionID: "s", Kind: NewsInfo, Title: fmt.Sprint(i), Origin: "one-invocation"}
					if keyed {
						in.Key, in.Origin = "deploy", fmt.Sprint(i)
					}
					_, err := r.PublishNews(bg, in)
					errCh <- err
				}()
			}
			close(start)
			wg.Wait()
			close(errCh)
			for err := range errCh {
				if err != nil {
					t.Fatal(err)
				}
			}
			list, err := b.ListNews(bg)
			if err != nil || len(list) != 1 {
				t.Fatalf("list=%+v err=%v", list, err)
			}
			wantRevision := int64(1)
			if keyed {
				wantRevision = writers
			}
			if list[0].Revision != wantRevision {
				t.Fatalf("revision=%d want=%d", list[0].Revision, wantRevision)
			}
		})
	}
}

func TestReviewNewsOldInvocationCannotOverwriteLaterUpdate(t *testing.T) {
	r := newRepo(t)
	firstInput := NewsInput{SessionID: "s", Key: "deploy", Kind: NewsProblem, Title: "Deploy broken", Origin: "m1/c1"}
	first := mustPublish(t, r, firstInput)
	latest := mustPublish(t, r, NewsInput{SessionID: "s", Key: "deploy", Kind: NewsDone, Title: "Deploy fixed", Origin: "m2/c1"})
	if _, err := r.ReadNews(bg, []NewsAck{{ID: latest.ID, Revision: latest.Revision}}); err != nil {
		t.Fatal(err)
	}
	retry := mustPublish(t, r, firstInput)
	t.Logf("first=%+v latest=%+v retry=%+v", first, latest, retry)
	if retry.Outcome != NewsDuplicate || retry.Revision != latest.Revision || retry.Title != latest.Title || retry.ReadAt == 0 {
		t.Fatalf("an older invocation retried after a keyed update rewrote the news: %+v", retry)
	}
}
