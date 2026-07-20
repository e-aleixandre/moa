import {
  UserWaypoint,
  AssistantDocument,
  CodeBlock,
  PermissionCard,
  PermissionControl,
  MobileLedger,
  LedgerIcons,
} from "../components/index.js";
import { MobileHeader } from "../layout/mobile/MobileHeader/MobileHeader.jsx";
import { SessionStrip } from "../layout/mobile/SessionStrip/SessionStrip.jsx";
import { SessionDrawer } from "../layout/mobile/SessionDrawer/SessionDrawer.jsx";
import "../layout/mobile/MobileConversationScreen/MobileConversationScreen.css";
import "./mobile-gallery.css";

// mobile-gallery — the MOCK design specimens for the mobile screen. In 5I the
// real MobileConversationScreen became a store-connected container (needs a
// populated store + a live WS), which the static design gallery can't provide.
// To keep the design surface reviewable WITHOUT a backend, the former 4A mock
// (hardcoded sessions / ledgers / conversation) lives HERE as
// MobileConversationSpecimen, decoupled from the connected container. It never
// touches the store — it renders the presentational chrome (MobileHeader /
// SessionStrip) around a hand-built AssistantDocument with mock MobileLedgers,
// plus a static MobileComposerSpecimen (the connected MobileComposer wraps the
// store-bound Composer, so the gallery uses a dumb stand-in for it).

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

// TAIL_ROWS / TAIL_LIVE — a live batch for the mobile B·Tail console-tail view:
// folded "N earlier actions" header (+ red error count), one terminated line,
// and the live line.
const TAIL_ROWS = [
  { id: "tr1", name: "read", action: "pkg/serve/ws.go", result: "130 ln", status: "ok" },
  { id: "tr2", name: "grep", action: '"Subscribe("', result: "7", status: "ok" },
  { id: "tr3", name: "bash", action: "go vet ./pkg/serve/", result: "error", status: "err" },
  { id: "tr4", name: "edit", action: "pkg/serve/ws.go", result: "+5 −3", status: "ok" },
];
const TAIL_LIVE = {
  id: "tr5",
  tool: "bash",
  arg: { text: "go test -race -count=50 ./pkg/serve/" },
  startedAt: Date.now() - 7000,
  liveTail: "=== RUN   TestResumeDelivery\n--- PASS: TestResumeDelivery (0.41s)\n=== RUN   TestResumeSnapshot",
};

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

// DRAWER_SESSIONS — mock data for the sessions bottom-sheet (mockup Phone 2).
export const DRAWER_SESSIONS = [
  {
    id: "deploy",
    title: "deploy pulse api",
    state: "permission",
    when: "now",
    needsLabel: "Needs you:",
    last: "allow `systemctl --user restart pulse-api`?",
    path: "~/dev/moa/pulse-api",
    unseen: true,
  },
  {
    id: "ws",
    title: "ws race fix",
    state: "running",
    when: "now",
    last: "Running full test suite after the resume() fix…",
    path: "~/dev/moa/main",
    active: true,
  },
  {
    id: "frontend",
    title: "frontend polish",
    state: "idle",
    when: "2h",
    last: "Done — pushed 3 commits, esbuild output rebuilt.",
    path: "~/dev/moa/frontend-polish",
  },
  {
    id: "sqlite",
    title: "migrate sqlite",
    state: "error",
    when: "18m",
    last: "provider 429 — retrying in 34s (attempt 3/5)",
    path: "~/dev/moa/migrate",
    unseen: true,
  },
  {
    id: "verifier",
    title: "verifier design notes",
    state: "saved",
    saved: true,
    when: "3d",
    last: "Saved · 84 messages",
    path: "~/dev/moa/main",
  },
];

const noop = () => {};

// MobileComposerSpecimen — a dumb stand-in for the connected MobileComposer
// (which wraps the store-bound Composer). Mirrors the old mock's editable
// textarea (font-size:var(--text-input) to avoid iOS zoom) + the redesigned
// status line (TELEMETRY-SETTINGS-REDESIGN §2): activity · live token pulse ·
// perm chip · spend, where spend is the Usage panel trigger. The per-run token
// pulse (↑ in / ↓ out) is the live heartbeat that the agent is working; session
// totals still live in the Usage panel.
function MobileComposerSpecimen({ status, perm = "yolo", spend, tokensUp, tokensDown }) {
  const hasTokens = tokensUp != null && tokensDown != null;
  return (
    <div class="mcomposer">
      <div class="mgal-composer-box">
        <textarea
          class="mgal-composer-input"
          rows={1}
          placeholder="Message moa…"
          aria-label="Message moa"
        />
        <button type="button" class="mgal-composer-send" aria-label="Send">
          ↑
        </button>
      </div>
      <div class="mcomposer-status">
        <span class="work">● {status}</span>
        {hasTokens && <span class="tokens">↑ {tokensUp} · ↓ {tokensDown}</span>}
        <PermissionControl mode={perm} sheet onChange={() => {}} />
        <button type="button" class="spend spend-btn" aria-label="Show usage">
          {spend} today
        </button>
      </div>
    </div>
  );
}

// MobileConversationSpecimen — the static mock of the mobile conversation
// screen for the gallery (decoupled from the connected container).
export function MobileConversationSpecimen() {
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

          <MobileLedger
            summary="B·Tail — live batch"
            icons={READ_ICONS}
            rows={TAIL_ROWS}
            liveRow={TAIL_LIVE}
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

      <MobileComposerSpecimen status="running tests" perm="yolo" spend="$1.84" tokensUp="41.2k" tokensDown="8.7k" />
    </div>
  );
}

// DrawerSpecimen — the conversation specimen with the sessions drawer forced
// open on top of it, so the gallery can show the bottom-sheet statically.
function DrawerSpecimen() {
  return (
    <>
      <MobileConversationSpecimen />
      <SessionDrawer
        open
        onClose={noop}
        sessions={DRAWER_SESSIONS}
        activeCount={4}
        savedCount={2}
        onSelect={noop}
        onNew={noop}
        onCloseSession={noop}
        onReopenSession={noop}
        onDeleteSession={noop}
      />
    </>
  );
}

// MobileGallery — shows the mobile conversation specimen (sub-phase 4A) and the
// sessions drawer (sub-phase 4B) inside realistic phone frames (notch, rounded
// corners, shadow) laid out side by side.
export function MobileGallery() {
  return (
    <div class="mgal">
      <header class="mgal-head">
        <h1>
          moa studio · <em>mobile</em>
        </h1>
        <p>
          The full-screen conversation on the phone: session header, session
          strip, touch stream with a 3-level ledger, and an anti-zoom composer.
          Everything breathes on its own.
        </p>
      </header>

      <div class="mgal-frames">
        <figure class="mgal-figure">
          <div class="mgal-device">
            <span class="mgal-notch" aria-hidden="true" />
            <div class="mgal-screen">
              <MobileConversationSpecimen />
            </div>
          </div>
          <figcaption class="mgal-caption">
            Conversation — full-screen session
          </figcaption>
        </figure>

        <figure class="mgal-figure">
          <div class="mgal-device">
            <span class="mgal-notch" aria-hidden="true" />
            <div class="mgal-screen">
              <DrawerSpecimen />
            </div>
          </div>
          <figcaption class="mgal-caption">
            Overview drawer — swipe down to close
          </figcaption>
        </figure>
      </div>
    </div>
  );
}
