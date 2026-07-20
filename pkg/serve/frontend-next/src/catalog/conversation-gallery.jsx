import { useState } from "preact/hooks";
import {
  UserWaypoint,
  AssistantDocument,
  ActivityLedger,
  CodeBlock,
  DiffBlock,
} from "../components/index.js";
import { fuseLedgerDetails } from "../data/util/ledger-details.jsx";
import "./conversation-gallery.css";

// A bash row carrying its full (multiline) command + output, fused through the
// REAL fuseLedgerDetails so the specimen exercises the shipped command-detail
// path (command first, divider, output below) instead of a hand-built node.
const LEDGER_CMD_ROWS = fuseLedgerDetails(
  [
    {
      id: "cd1",
      tool: "bash",
      arg: { text: "tail -30 /home/ealeixandre/.local/state/moa/…" },
      out: "30 lines",
      status: "ok",
      command: "tail -30 /home/ealeixandre/.local/state/moa/update.log",
      body:
        "===================== 2026-07-20 19:33:39 UTC update OK ✔ branch=feat/serve-redesign =====================\n" +
        "[7/7] reiniciando moa + healthcheck…\n  backup: /usr/local/bin/moa.bak.20260720-193333",
    },
    {
      id: "cd2",
      tool: "bash",
      arg: { text: "cd /home/ealeixandre/dev/moa/serve-redesign && git…" },
      out: "ok",
      status: "ok",
      command:
        "cd /home/ealeixandre/dev/moa/serve-redesign && \\\n" +
        "  git add pkg/serve/frontend-next/src pkg/serve/static-next && \\\n" +
        '  git commit -m "feat(serve): playful working verbs + shimmer"',
      body:
        "[feat/serve-redesign 12a6c60] feat(serve): playful working verbs + shimmer\n" +
        " 9 files changed, 204 insertions(+), 87 deletions(-)",
    },
  ],
  null,
);

const GO_SAMPLE = `func (c *client) resume(sess *Session, from uint64) error {
	ch := sess.Bus.Subscribe(c.id) // subscribe BEFORE snapshot
	snap, last := sess.Log.Snapshot(from)
	c.sendAll(snap)
	// drain events already covered by the snapshot
	c.forward(ch, func(e Event) bool { return e.Seq > last })
	return c.stream(ch)
}`;

const DIFF_TEXT = `--- a/pkg/serve/ws.go
+++ b/pkg/serve/ws.go
@@ -262,8 +262,9 @@
 func (c *client) resume(sess *Session, from uint64) error {
-	snap, last := sess.Log.Snapshot(from)
-	c.sendAll(snap)
-	ch := sess.Bus.Subscribe(c.id)
+	ch := sess.Bus.Subscribe(c.id) // subscribe BEFORE snapshot
+	snap, last := sess.Log.Snapshot(from)
+	c.sendAll(snap)
+	// drain events already covered by the snapshot
+	c.forward(ch, func(e Event) bool { return e.Seq > last })
 	return c.stream(ch)
 }`;

const LEDGER_READ_ROWS = [
  {
    id: "rd1",
    tool: "read",
    arg: { text: "pkg/serve/ws.go", detail: "lines 210–340" },
    out: "130 lines",
    status: "ok",
  },
  {
    id: "rd2",
    tool: "grep",
    arg: '"Subscribe(" — pkg/bus/',
    out: "7 matches",
    status: "ok",
  },
  {
    id: "rd3",
    tool: "bash",
    arg: { text: "go test -race -run TestResume ./pkg/serve/", detail: "×20" },
    out: "1 failure",
    status: "err",
    detail: {
      node: (
        <div class="doc-mono">
          <span class="dim">
            --- FAIL: TestResumeDelivery (0.31s)  <span class="y">run 14/20</span>
          </span>
          {"\n"}
          {"    ws_test.go:88: missed event: seq=1042 (snapshot ended at 1041,\n"}
          {"    subscription started at 1043)\n"}
          <span class="r">FAIL</span>
          {"\tgithub.com/ealeixandre/moa/pkg/serve\t0.412s"}
        </div>
      ),
    },
  },
];

const LEDGER_FIX_ROWS = [
  {
    id: "fx1",
    tool: "edit",
    arg: { text: "pkg/serve/ws.go", detail: "resume()" },
    out: "+5 −3",
    status: "ok",
    // Fused diff: opens INSIDE the card as a borderless recessed panel (the
    // real stream fuses the edit's `diff` sibling here via fuseLedgerDetails).
    detail: { node: <DiffBlock className="flush" diffText={DIFF_TEXT} filename="pkg/serve/ws.go" /> },
  },
  {
    id: "fx2",
    tool: "edit",
    arg: {
      text: "pkg/serve/ws_test.go",
      detail: "TestResumeDelivery: publish during snapshot",
    },
    out: "+18",
    status: "ok",
  },
  {
    id: "fx3",
    tool: "bash",
    arg: "go test -race -count=50 -run TestResume ./pkg/serve/",
    out: "ok · 3.9s",
    status: "ok",
  },
];

// LEDGER_TAIL_ROWS — a live batch (>3 calls, last one still running) to
// exercise the unified card's folded live phase: a folded "N earlier actions"
// header (with a red error count), the last terminated rows, and the live row.
const LEDGER_TAIL_ROWS = [
  { id: "t1", tool: "read", arg: { text: "pkg/serve/ws.go" }, out: "130 lines", status: "ok" },
  { id: "t2", tool: "grep", arg: '"Subscribe(" — pkg/bus/', out: "7 matches", status: "ok" },
  { id: "t3", tool: "bash", arg: { text: "go vet ./pkg/serve/" }, out: "1 issue", status: "err" },
  { id: "t4", tool: "edit", arg: { text: "pkg/serve/ws.go", detail: "resume()" }, out: "+5 −3", status: "ok" },
  { id: "t5", tool: "bash", arg: { text: "chmod +x scripts/deploy.sh" }, out: "rejected", status: "warn" },
  {
    id: "t6",
    tool: "bash",
    arg: { text: "go test -race -count=50 -run TestResume ./pkg/serve/" },
    live: true,
    startedAt: Date.now() - 7000,
    liveTail: "=== RUN   TestResumeDelivery\n--- PASS: TestResumeDelivery (0.41s)\n=== RUN   TestResumeSnapshot",
  },
];

// ConversationGallery — reproduces the Studio mockup flow (conversation-
// desktop.html): user waypoint, assistant document with ledgers and
// code blocks, and the streaming cursor at the end of the last paragraph.
export function ConversationGallery() {
  const [streaming, setStreaming] = useState(true);
  return (
    <section>
      <h2>Primitivas de conversación</h2>
      <p class="lead">
        Registro de trabajo ("Studio"): el prompt del usuario es un waypoint,
        el trabajo del agente fluye como documento, las tool calls van en un
        activity ledger colapsable.
      </p>

      <div class="conv-stream">
        <UserWaypoint time="10:12" html={'<p>XSS test<img src=x onerror="window.__xssFired=true"></p>'} />
        <UserWaypoint time="10:12">
          <p>
            The web client sometimes drops the first event after reconnect. I
            think there's a race in <code>pkg/serve/ws.go</code> between the
            resume snapshot and the live subscription. Find it and fix it,
            with a regression test.
          </p>
        </UserWaypoint>

        <AssistantDocument streaming={streaming}>
          <p>
            Reading the reconnect path first to see how the snapshot and the
            subscription are sequenced.
          </p>

          <ActivityLedger rows={LEDGER_READ_ROWS} />

          <p>
            Confirmed — it's a classic gap: <code>sendSnapshot</code> reads
            the log up to <code>seq N</code>, then <code>Subscribe</code>{" "}
            registers <em>after</em> the read lock is released. Any event
            published in that window is lost. The fix is to subscribe first,
            then snapshot, then drain the subscription discarding{" "}
            <code>seq ≤ N</code>:
          </p>

          <ActivityLedger rows={LEDGER_FIX_ROWS} />

          <p>
            <strong>Fixed.</strong> Subscription now opens before the
            snapshot read, and duplicates are dropped by sequence number, so
            the window is closed on both sides. The stress test passes 50/50
            with <code>-race</code>. Now running the full suite to be safe
          </p>
        </AssistantDocument>

        <CodeBlock code={GO_SAMPLE} lang="go" filename="pkg/serve/ws.go · resume()" />

        <h3 class="conv-sub">Unified card — live tool batch</h3>
        <p class="lead">
          A batch with more than three calls whose last tool is still running:
          a folded "N earlier actions" header (red error count when a failure
          is folded away), the last terminated lines (✓ green / ! yellow
          rejected / ✗ red error), and the live line with a blinking caret and
          elapsed timer.
        </p>
        <div class="conv-stream">
          <AssistantDocument streaming={false}>
            <ActivityLedger rows={LEDGER_TAIL_ROWS} />
          </AssistantDocument>
        </div>

        <h3 class="conv-sub">Unified card — full command on expand</h3>
        <p class="lead">
          Open a bash row and the full command shows first (mono, wrapping,
          multiline preserved), a thin divider, then its output below. The
          collapsed row stays a short ellipsised summary.
        </p>
        <div class="conv-stream">
          <AssistantDocument streaming={false}>
            <ActivityLedger rows={LEDGER_CMD_ROWS} />
          </AssistantDocument>
        </div>
      </div>

      <label class="conv-stream-toggle">
        <input
          type="checkbox"
          checked={streaming}
          onChange={(e) => setStreaming(e.currentTarget.checked)}
        />
        streaming cursor
      </label>
    </section>
  );
}
