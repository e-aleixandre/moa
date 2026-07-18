import {
  UserWaypoint,
  AssistantDocument,
  ActivityLedger,
  DiffBlock,
  PermissionCard,
} from "../../components/index.js";
import "./Stream.css";

// Stream — the scrollable conversation area: date separator, user prompt,
// assistant document with ledgers/diff interleaved, and the pending
// permission card. Mock data faithful to the conversation-desktop.html
// mockup, translated to English.
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

export function Stream({ streaming = true }) {
  return (
    <div class="stream">
      <div class="stream-col">
        <div class="stream-tick">TODAY · 10:12</div>

        <UserWaypoint time="10:12">
          <p>
            The web client sometimes drops the first event after reconnect. I
            think there's a race in <code>pkg/serve/ws.go</code> between the
            resume snapshot and the live subscription. Find it and fix it,
            with a regression test.
          </p>
        </UserWaypoint>

        <AssistantDocument>
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
        </AssistantDocument>

        <AssistantDocument streaming={streaming}>
          <p>
            <strong>Fixed.</strong> Subscription now opens before the
            snapshot read, and duplicates are dropped by sequence number, so
            the window is closed on both sides. The stress test passes 50/50
            with <code>-race</code>. Now running the full suite to be safe
          </p>
        </AssistantDocument>

        <PermissionCard
          title="moa wants to run"
          command="go test -race ./... && go vet ./..."
          timer="waiting 0:07"
          alwaysLabel="go test"
        />
      </div>
    </div>
  );
}
