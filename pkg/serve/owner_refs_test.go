package serve

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/owner"
)

// TestOwnerRefSurvivesAnInvalidationMidResolution drives the window the memo
// used to have: a resolution reads "this directory has no owner" from disk,
// an owner is created while that answer is in flight, and the answer is
// written into the memo afterwards — on top of the invalidation the creation
// fired. The memo then answered "no owner" until the next owner was created
// or deleted, so every session of that codebase looked unwatched: no owner in
// the API, and a stray in another owner's roster.
//
// The interleaving is not left to luck. Against the old code — which read
// disk with ownerRefMu released — holding that lock parks the resolution
// exactly where it would publish, and this test fails every run. Now the lock
// is held across the resolution, so the scenario cannot even be built: the
// creation below waits for the resolution to finish. What the test guards is
// the property, not the mechanism.
func TestOwnerRefSurvivesAnInvalidationMidResolution(t *testing.T) {
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	store, err := mgr.ownerStore()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Resolves a codebase that has no owner on disk yet.
		mgr.ownerRefFor(root)
	}()

	// Long enough for the resolution to be under way (the git exec it runs
	// was measured at ~0.7 ms), short enough that it cannot have published
	// yet under the old code. Missing that window costs detection of the old
	// bug, never a false failure.
	time.Sleep(200 * time.Microsecond)
	mgr.ownerRefMu.Lock()
	time.Sleep(20 * time.Millisecond)

	own, err := store.Create(root, "Winerim", "", "", false, owner.Avatar{})
	if err != nil {
		mgr.ownerRefMu.Unlock()
		t.Fatal(err)
	}
	// What invalidateOwnerRefs does, with the lock already held here.
	mgr.ownerRefs = nil
	mgr.ownerRefMu.Unlock()
	wg.Wait()

	if ref := mgr.ownerRefFor(root); ref.id != own.ID {
		t.Fatalf("a resolution in flight wrote over the invalidation: owner ref = %q, want %q",
			ref.id, own.ID)
	}
}

// TestOwnerRefIsDroppedByACreationThatFailed covers the other way the memo can
// outlive the truth: CreateOwner only dropped it on the success path, so a
// creation that touched owner.json and then failed — the rollback after the
// session could not be built, a Store.Create that wrote the entity and failed
// to seed the book — left the memo answering from before the attempt.
func TestOwnerRefIsDroppedByACreationThatFailed(t *testing.T) {
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	store, err := mgr.ownerStore()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()

	// The memo learns there is no owner here.
	if ref := mgr.ownerRefFor(root); ref.id != "" {
		t.Fatalf("a fresh directory already has an owner: %q", ref.id)
	}
	// One appears on disk without going through CreateOwner, as another
	// process or a rolled back attempt would leave it.
	own, err := store.Create(root, "Winerim", "", "", false, owner.Avatar{})
	if err != nil {
		t.Fatal(err)
	}
	// The creation fails on the entity that is already there.
	if _, err := mgr.CreateOwner(CreateOwnerOpts{Root: root, Name: "Winerim"}); !errors.Is(err, owner.ErrExists) {
		t.Fatalf("creating over an existing owner: error = %v, want %v", err, owner.ErrExists)
	}
	if ref := mgr.ownerRefFor(root); ref.id != own.ID {
		t.Fatalf("a creation that failed left the memo behind: owner ref = %q, want %q", ref.id, own.ID)
	}
}
