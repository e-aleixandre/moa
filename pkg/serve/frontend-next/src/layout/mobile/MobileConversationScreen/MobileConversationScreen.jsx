import {
  UserWaypoint,
  AssistantDocument,
  CodeBlock,
  PermissionCard,
  MobileLedger,
  LedgerIcons,
} from "../../../components/index.js";
import { MobileHeader } from "../MobileHeader/MobileHeader.jsx";
import { SessionStrip } from "../SessionStrip/SessionStrip.jsx";
import { MobileComposer } from "../MobileComposer/MobileComposer.jsx";
import "./MobileConversationScreen.css";

const { FileText, Search, Pencil, Terminal } = LedgerIcons;

const SESSIONS = [
  { id: "ws", name: "ws race", state: "running" },
  { id: "deploy", name: "deploy", state: "permission", needs: true, unseen: true },
  { id: "frontend", name: "frontend", state: "idle" },
  { id: "sqlite", name: "sqlite", state: "error" },
];

const READ_ICONS = (
  <>
    <FileText size={13} aria-hidden="true" />
    <Search size={13} aria-hidden="true" />
  </>
);

const WORK_ICONS = (
  <>
    <Pencil size={12} aria-hidden="true" />
    <Terminal size={13} aria-hidden="true" />
  </>
);

const EDIT_DIFF = `@@ -262,6 +262,7 @@
 func (c *client) resume(…) error {
-  snap, last := sess.Log.Snapshot(from)
-  ch := sess.Bus.Subscribe(c.id)
+  ch := sess.Bus.Subscribe(c.id)
+  snap, last := sess.Log.Snapshot(from)
+  c.forward(ch, func(e Event) bool {
+    return e.Seq > last })
   return c.stream(ch)`;

const BASH_OUTPUT = `$ go test -race -count=50 -run TestResume ./pkg/serve/
ok  github.com/ealeixandre/moa/pkg/serve  3.912s
50/50 runs passed · no data races detected`;

const CODE_SAMPLE = `// subscribe BEFORE snapshot
ch := sess.Bus.Subscribe(c.id)
snap, last := sess.Log.Snapshot(from)
c.forward(ch, func(e Event) bool {
  return e.Seq > last
})`;

const READ_ROWS = [
  {
    id: "read",
    kind: "read",
    name: "read",
    action: "2 files · pkg/serve",
    result: "ok",
    detail: {
      type: "files",
      files: [
        { id: "ws", name: "pkg/serve/ws.go", lines: "lines 210–340" },
        { id: "ws-test", name: "pkg/serve/ws_test.go", lines: "all · 214 ln" },
      ],
    },
  },
];

const WORK_ROWS = [
  {
    id: "read",
    kind: "read",
    name: "read",
    action: "2 files · pkg/serve",
    result: "ok",
    detail: {
      type: "files",
      files: [
        { id: "ws", name: "pkg/serve/ws.go", lines: "lines 210–340" },
        { id: "ws-test", name: "pkg/serve/ws_test.go", lines: "all · 214 ln" },
      ],
    },
  },
  {
    id: "edit",
    kind: "edit",
    name: "edit",
    action: "pkg/serve/ws.go · resume()",
    result: "+5 −3",
    detail: {
      type: "diff",
      diffText: EDIT_DIFF,
      filename: "pkg/serve/ws.go",
      actions: ["open file", "copy diff"],
    },
  },
  {
    id: "bash",
    kind: "bash",
    name: "bash",
    action: "go test -race -count=50 ./pkg/serve/",
    result: "ok",
    detail: {
      type: "bash",
      output: BASH_OUTPUT,
      actions: ["full output", "re-run"],
    },
  },
];

const noop = () => {};

// MobileConversationScreen — root organism of the mobile conversation
// screen (sub-phase 4A). Combines header + session strip + scrollable stream +
// composer with mock data faithful to the mockup. The sessions drawer (4B) doesn't
// exist yet: onOpenSessions is a noop.
export function MobileConversationScreen() {
  return (
    <div class="mconv">
      <MobileHeader
        state="running"
        title="ws race fix"
        model="sol"
        level="high"
        path="~/dev/moa/main"
        ctx={62}
        onOpenSessions={noop}
      />
      <SessionStrip
        sessions={SESSIONS}
        activeId="ws"
        onSelect={noop}
        onNew={noop}
      />

      <div class="mconv-stream">
        <UserWaypoint time="10:41">
          <p>
            Fix the reconnect race in <code>ws.go</code>, with a regression
            test.
          </p>
        </UserWaypoint>

        <AssistantDocument>
          <p>
            Found it — the subscription registers <strong>after</strong> the
            snapshot read, so events in that window are lost.
          </p>

          <MobileLedger
            summary="read 2 files · searched pkg/bus"
            icons={READ_ICONS}
            rows={READ_ROWS}
          />

          <MobileLedger
            summary="2 edits · tests ok"
            icons={WORK_ICONS}
            rows={WORK_ROWS}
            defaultOpen
            defaultOpenRowIds={["read", "edit", "bash"]}
          />

          <CodeBlock code={CODE_SAMPLE} lang="go" showHeader={false} />

          <p>
            <strong>Fixed</strong> — stress test passes 50/50 with{" "}
            <code>-race</code>. One thing before the full suite:
          </p>
        </AssistantDocument>

        <PermissionCard
          title="Allow this command?"
          command="go test -race ./... && go vet ./..."
          timer="0:07"
          alwaysLabel="always allow go test"
          onAllow={noop}
          onAlways={noop}
          onDeny={noop}
        />
      </div>

      <MobileComposer
        status="running tests"
        up="41k"
        down="8.7k"
        spend="$1.84"
      />
    </div>
  );
}
