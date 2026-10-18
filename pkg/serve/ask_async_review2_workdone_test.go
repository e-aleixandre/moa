package serve

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
)

// Crash with an acknowledged admission receipt but without a durable terminal
// snapshot. SQLite must not claim the answer's continuation is done merely
// because the final model text exists in RAM.
func TestPersistenceReviewWorkDoneRequiresTerminalSnapshot(t *testing.T) {
	if os.Getenv(f1CrashChildEnv) == "review-workdone" {
		reviewWorkDoneChild(t)
		return
	}
	d := f1NewCrashDirs(t)
	t.Setenv("MOA_CONFIG_DIR", d.config)
	ctx, cancel := context.WithTimeout(context.Background(), f1CrashChildBudget)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPersistenceReviewWorkDoneRequiresTerminalSnapshot$", "-test.timeout=75s", "-test.count=1")
	for _, env := range os.Environ() {
		if !strings.HasPrefix(env, "MOA_CONFIG_DIR=") {
			cmd.Env = append(cmd.Env, env)
		}
	}
	cmd.Env = append(cmd.Env, f1CrashChildEnv+"=review-workdone", f1CrashBaseEnv+"="+d.base, f1CrashWorkspaceEnv+"="+d.workspace, f1CrashConfigEnv+"="+d.config, f1CrashManifestEnv+"="+d.manifest, "TMPDIR="+d.tmp)
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.CombinedOutput()
	t.Logf("crash child:\n%s", out)
	if err != nil {
		t.Fatalf("child failed before intended crash: %v", err)
	}
	raw, err := os.ReadFile(d.manifest)
	if err != nil {
		t.Fatal(err)
	}
	var manifest f1CrashManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	// Only after the process is dead: put back the last durable snapshot,
	// which ends at the admitted answer. No live writer can repair it now.
	dirBytes, err := os.ReadFile(filepath.Join(d.config, "review-blocked-directory"))
	if err != nil {
		t.Fatal(err)
	}
	dir := string(dirBytes)
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(dir+".review-last-durable", dir); err != nil {
		t.Fatal(err)
	}
	saved, _, err := session.FindSessionReadOnly(d.base, manifest.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(f1Answers(treeMessages(saved))); got != 1 {
		t.Fatalf("precondition: saved answer events=%d", got)
	}
	for _, en := range saved.Entries {
		if strings.Contains(f1MsgText(en.Message), "F1-UNSAVED-FINAL") {
			t.Fatal("invalid fixture: final response was persisted")
		}
	}
	prov := &f1Provider{script: f1Script(false)}
	e := f1NewEnv(t, d.base, d.workspace, prov)
	q, err := e.mgr.tasks.GetQuestion(context.Background(), manifest.QuestionID)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("after crash: SQLite work=%s; durable transcript has E but no final response", q.Work)
	if !f1Eventually(f1Guard, func() bool { return prov.countWhere(f1LastUserIsAnswer) > 0 }) {
		t.Fatalf("lost recovery: work=%s, persisted answer but no durable terminal snapshot, recovery model calls=%d", q.Work, prov.count())
	}
}

func reviewWorkDoneChild(t *testing.T) {
	base, root, cfg := os.Getenv(f1CrashBaseEnv), os.Getenv(f1CrashWorkspaceEnv), os.Getenv(f1CrashConfigEnv)
	t.Setenv("MOA_CONFIG_DIR", cfg)
	var sess *ManagedSession
	script := f1Script(false)
	p := &f1Provider{script: func(req core.Request) <-chan core.AssistantEvent {
		if !f1LastUserIsAnswer(req) {
			return script(req)
		}
		dir := sess.persister.store.Dir()
		if !sess.persister.has(func(s *session.Session) bool { return len(f1Answers(treeMessages(s))) == 1 }) {
			panic("admission receipt was not saved before inference")
		}
		if err := os.Rename(dir, dir+".review-last-durable"); err != nil {
			panic(err)
		}
		if err := os.WriteFile(dir, []byte("real FileStore fault after acknowledged admission"), 0o600); err != nil {
			panic(err)
		}
		if err := os.WriteFile(filepath.Join(cfg, "review-blocked-directory"), []byte(dir), 0o600); err != nil {
			panic(err)
		}
		return f1Text("F1-UNSAVED-FINAL")
	}}
	mgr := f1CrashManager(base, root, cfg, p)
	e := &f1Env{t: t, mgr: mgr, prov: p, root: root, srv: newF1ChildServer(mgr)}
	sess = e.session(f1KindHuman)
	e.send(sess, "F1-TRIGGER please ask")
	qid := waitPendingAsk(t, sess).ID
	f1Settle(t, sess)
	if code, body := f1PostAsk(t, e, sess.ID, qid, "yes"); code != 204 {
		t.Fatalf("answer=%d %s", code, body)
	}
	// The reviewer's fixture waited for work=done here, which is the very
	// defect. What it needs is the run that read the answer to be over.
	pollUntil(t, f1Guard, "the model to read the answer", func() bool { return p.countWhere(f1LastUserIsAnswer) > 0 })
	f1Settle(t, sess)
	if err := sess.runtime.Flush(); err == nil {
		t.Fatal("invalid fixture: terminal snapshot unexpectedly saved")
	}
	f1CrashNow(t, sess.ID, qid)
}
