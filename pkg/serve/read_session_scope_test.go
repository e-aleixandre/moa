package serve

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/session"
)

// A04: a declared reference is necessary, never sufficient. Foreign codebase,
// self, an owner conversation and an unknown ID are all declared by the human
// and all refused by the tool; the same-codebase control proves the receipt
// path itself works so the refusals are not blanket.
func TestReadSessionDeclaredButIneligibleTargetsAreRefused(t *testing.T) {
	env := newRSEnv(t, nil)
	_, ownerSess := ownerWithSession(t, env.mgr, env.root, "Proj")
	appendConversationTestMessage(ownerSess, "o1", "user", "OWNER_SENT", nil)
	caller, control := env.session(env.root), env.target("CONTROL_SENT")
	foreign := env.session(t.TempDir())
	appendConversationTestMessage(foreign, "f1", "user", "FOREIGN_SENT", nil)
	unknown := strings.Repeat("0", 24)

	env.mention(caller, "all of them", control.ID, foreign.ID, caller.ID, ownerSess.ID, unknown)

	rsExpectRead(t, caller, control.ID, "CONTROL_SENT")
	rsExpectDenied(t, caller, foreign.ID, "FOREIGN_SENT")
	rsExpectDenied(t, caller, caller.ID)
	rsExpectDenied(t, caller, ownerSess.ID, "OWNER_SENT")
	rsExpectDenied(t, caller, unknown)
	rsExpectDenied(t, caller, "sa-0123456789ab") // a subagent job id is never a conversation
}

// A07: identity is exact. A declared upper-case spelling does not open the
// lower-case ID and the other way round; no prefix or alias resolution.
func TestReadSessionIdentityIsExact(t *testing.T) {
	env := newRSEnv(t, nil)
	caller, target := env.session(env.root), env.target("T_SENT")
	// An all-digit ID has no case to fold: draw another session until the
	// contrast exists (a valid ID is never a failure).
	for strings.ToUpper(target.ID) == target.ID {
		target = env.target("T_SENT")
	}
	upper := strings.ToUpper(target.ID)
	env.mention(caller, "upper", upper)
	rsExpectDenied(t, caller, target.ID, "T_SENT")
	rsExpectDenied(t, caller, upper, "T_SENT")

	env.mention(caller, "exact", target.ID)
	rsExpectRead(t, caller, target.ID, "T_SENT")
	rsExpectDenied(t, caller, target.ID[:12], "T_SENT")
}

// A05: a saved target is readable without being resumed or written: the real
// JSON file keeps its bytes and mtime and no runtime appears.
func TestReadSessionReadsSavedTargetWithoutResuming(t *testing.T) {
	env := newRSEnv(t, nil)
	caller, target := env.session(env.root), env.target("T_SENT")
	if err := env.mgr.CloseSession(target.ID); err != nil {
		t.Fatal(err)
	}
	env.mention(caller, "saved", target.ID)
	path := rsSavedPath(t, env, target.ID)
	beforeBytes, beforeInfo := rsFileState(t, path)

	rsExpectRead(t, caller, target.ID, "T_SENT")

	afterBytes, afterInfo := rsFileState(t, path)
	if string(beforeBytes) != string(afterBytes) || !beforeInfo.ModTime().Equal(afterInfo.ModTime()) {
		t.Error("reading a saved target rewrote its file")
	}
	if _, live := env.mgr.Get(target.ID); live {
		t.Error("reading resumed the saved target")
	}
}

// A06/I16: a detached session is readable by a human mention from a normal
// conversation, while the owner keeps its stricter no-detached rule and the
// mention does not reattach it.
func TestReadSessionDetachedTargetReadableButOwnerStaysStrict(t *testing.T) {
	env := newRSEnv(t, nil)
	_, ownerSess := ownerWithSession(t, env.mgr, env.root, "Proj")
	child, err := env.mgr.CreateSession(CreateOpts{CWD: env.root, Origin: "owner", Title: "child"})
	if err != nil {
		t.Fatal(err)
	}
	appendConversationTestMessage(child, "c1", "user", "DETACHED_SENT", nil)
	if _, err := env.mgr.SetOwnerDetached(child.ID, true); err != nil {
		t.Fatal(err)
	}
	caller := env.session(env.root)
	env.mention(caller, "detached", child.ID)

	rsExpectRead(t, caller, child.ID, "DETACHED_SENT")

	res := runSessionsTool(t, ownerSess, map[string]any{"action": "read", "session_id": child.ID})
	if !res.IsError || strings.Contains(toolText(res), "DETACHED_SENT") {
		t.Errorf("owner read of a detached session = error %v %q", res.IsError, toolText(res))
	}
}

// A08: deleting the target after the mention revokes in effect; read options
// do not bypass it and nothing is resurrected.
func TestReadSessionDeletedTargetIsRefused(t *testing.T) {
	env := newRSEnv(t, nil)
	caller, target := env.session(env.root), env.target("T_SENT")
	env.mention(caller, "soon gone", target.ID)
	rsExpectRead(t, caller, target.ID, "T_SENT")
	if err := env.mgr.Delete(target.ID); err != nil {
		t.Fatal(err)
	}
	for _, extra := range []map[string]any{
		{},
		{"message_id": rsTargetMsgID},
		{"tools": true},
		{"offset": 0, "limit": 5},
	} {
		params := map[string]any{"session_id": target.ID}
		for k, v := range extra {
			params[k] = v
		}
		rsCheckDenied(t, target.ID, rsRead(t, caller, params), []string{"T_SENT"})
	}
	if _, live := env.mgr.Get(target.ID); live {
		t.Error("a deleted target came back")
	}
}

// Moved target: a SAVED target whose real file now points at another codebase,
// while the cached roster still shows the old CWD. Authorization must come from
// the data that is read, not from the roster. The moved target is also refused
// under every read option, and a control proves the mention itself worked.
func TestReadSessionSavedTargetMovedAfterMentionIsRefused(t *testing.T) {
	env := newRSEnv(t, nil)
	env.mgr.savedCacheTTL = time.Hour // keep the stale roster without sleeping
	caller, target, control := env.session(env.root), env.target("MOVED_SENT"), env.target("CONTROL_SENT")
	if err := env.mgr.CloseSession(target.ID); err != nil {
		t.Fatal(err)
	}
	env.mention(caller, "both", target.ID, control.ID)
	rsExpectRead(t, caller, target.ID, "MOVED_SENT")
	rsExpectRead(t, caller, control.ID, "CONTROL_SENT")

	var warm *SessionInfo
	for _, info := range env.mgr.List() { // warm the roster cache
		if info.ID == target.ID {
			warm = &info
		}
	}
	if warm == nil || warm.State != StateSaved || warm.CWD != env.root {
		t.Fatalf("fixture: roster entry = %+v, want saved at %s", warm, env.root)
	}

	sess, store, err := session.FindSessionReadOnly(env.mgr.sessionBaseDir, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	sess.Metadata[session.MetaCWD] = other
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}
	fresh, _, err := session.FindSessionReadOnly(env.mgr.sessionBaseDir, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cwd, _ := fresh.Metadata[session.MetaCWD].(string); cwd != other {
		t.Fatalf("fixture: disk cwd = %q, want %q", cwd, other)
	}
	for _, info := range env.mgr.List() {
		if info.ID == target.ID && (info.State != StateSaved || info.CWD != env.root) {
			t.Fatalf("fixture: roster should still be stale, got %+v", info)
		}
	}
	if _, live := env.mgr.Get(target.ID); live {
		t.Fatal("fixture: target is live")
	}

	rsExpectOptionsDenied(t, caller, target.ID, rsTargetMsgID, "MOVED_SENT", "")
	rsExpectRead(t, caller, control.ID, "CONTROL_SENT")
	if _, live := env.mgr.Get(target.ID); live {
		t.Error("a refused read resumed the target")
	}
}

// Options never bypass the mention: an existing same-codebase target that was
// not mentioned stays unreadable under every option, while another target
// mentioned over HTTP is readable (so this is not a blanket deny).
func TestReadSessionOptionsDoNotBypassMissingMention(t *testing.T) {
	env := newRSEnv(t, nil)
	caller, allowed, hidden := env.session(env.root), env.target("ALLOWED_SENT"), env.target("HIDDEN_SENT")
	env.mention(caller, "just one", allowed.ID)
	rsExpectRead(t, caller, allowed.ID, "ALLOWED_SENT")
	rsExpectOptionsDenied(t, caller, hidden.ID, rsTargetMsgID, "HIDDEN_SENT", "mentioned")
}

func rsSavedPath(t *testing.T, env *rsEnv, id string) string {
	t.Helper()
	_, store, err := session.FindSessionReadOnly(env.mgr.sessionBaseDir, id)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(store.Dir(), id+".json")
}

func rsFileState(t *testing.T, path string) ([]byte, os.FileInfo) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return b, info
}
