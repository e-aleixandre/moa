import { useState } from "preact/hooks";
import {
  UserWaypoint,
  AssistantDocument,
  ActivityLedger,
  CodeBlock,
  DiffBlock,
} from "../components/index.js";
import "./conversation-gallery.css";

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
    tool: "read",
    arg: { text: "pkg/serve/ws.go", detail: "lines 210–340" },
    out: "130 lines",
    status: "ok",
  },
  {
    tool: "grep",
    arg: '"Subscribe(" — pkg/bus/',
    out: "7 matches",
    status: "ok",
  },
  {
    tool: "bash",
    arg: { text: "go test -race -run TestResume ./pkg/serve/", detail: "×20" },
    out: "1 failure",
    status: "err",
    defaultOpen: true,
    body: (
      <>
        <span class="dim">
          --- FAIL: TestResumeDelivery (0.31s)  <span class="y">run 14/20</span>
        </span>
        {"\n"}
        {"    ws_test.go:88: missed event: seq=1042 (snapshot ended at 1041,\n"}
        {"    subscription started at 1043)\n"}
        <span class="r">FAIL</span>
        {"\tgithub.com/ealeixandre/moa/pkg/serve\t0.412s"}
      </>
    ),
  },
];

const LEDGER_FIX_ROWS = [
  {
    tool: "edit",
    arg: { text: "pkg/serve/ws.go", detail: "resume()" },
    out: "+5 −3",
    status: "ok",
  },
  {
    tool: "edit",
    arg: {
      text: "pkg/serve/ws_test.go",
      detail: "TestResumeDelivery: publish during snapshot",
    },
    out: "+18",
    status: "ok",
  },
  {
    tool: "bash",
    arg: "go test -race -count=50 -run TestResume ./pkg/serve/",
    out: "ok · 3.9s",
    status: "ok",
  },
];

// LEDGER_TAIL_ROWS — a live batch (>3 calls, last one still running) to
// exercise the B·Tail console-tail view: folded "N earlier actions" header
// (with a red error count), the last terminated lines, and the live line.
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

          <DiffBlock diffText={DIFF_TEXT} filename="pkg/serve/ws.go" />

          <ActivityLedger rows={LEDGER_FIX_ROWS} />

          <p>
            <strong>Fixed.</strong> Subscription now opens before the
            snapshot read, and duplicates are dropped by sequence number, so
            the window is closed on both sides. The stress test passes 50/50
            with <code>-race</code>. Now running the full suite to be safe
          </p>
        </AssistantDocument>

        <CodeBlock code={GO_SAMPLE} lang="go" filename="pkg/serve/ws.go · resume()" />

        <h3 class="conv-sub">B·Tail — live tool batch (console tail)</h3>
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
