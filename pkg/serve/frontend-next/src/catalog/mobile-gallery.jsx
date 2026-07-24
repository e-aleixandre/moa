import {
  UserWaypoint,
  AssistantDocument,
  ActivityLedger,
  CodeBlock,
  DiffBlock,
  PermissionCard,
  PermissionControl,
  TokenFlow,
} from "../components/index.js";
import { MobileHeader } from "../layout/mobile/MobileHeader/MobileHeader.jsx";
import { SessionStrip } from "../layout/mobile/SessionStrip/SessionStrip.jsx";
import { SessionDrawer } from "../layout/mobile/SessionDrawer/SessionDrawer.jsx";
import { MobileSubagentView } from "../layout/mobile/MobileConversationScreen/MobileSubagentView.jsx";
import "../layout/mobile/MobileConversationScreen/MobileConversationScreen.css";
import "./mobile-gallery.css";

// mobile-gallery — the MOCK design specimens for the mobile screen. In 5I the
// real MobileConversationScreen became a store-connected container (needs a
// populated store + a live WS), which the static design gallery can't provide.
// To keep the design surface reviewable WITHOUT a backend, the former 4A mock
// (hardcoded sessions / ledgers / conversation) lives HERE as
// MobileConversationSpecimen, decoupled from the connected container. It never
// touches the store — it renders the presentational chrome (MobileHeader /
// SessionStrip) around a hand-built AssistantDocument with mock tool-group
// cards (<ActivityLedger>), plus a static MobileComposerSpecimen (the connected
// MobileComposer wraps the store-bound Composer, so the gallery uses a dumb
// stand-in for it).

const SESSIONS = [
  { id: "ws", name: "ws race", state: "running" },
  { id: "deploy", name: "deploy", state: "permission", needs: true, unseen: true },
  { id: "frontend", name: "frontend", state: "idle" },
  { id: "sqlite", name: "sqlite", state: "error" },
];

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

// Ledger rows in the projectStream shape ({ tool, arg:{text}, out, status, id,
// body?, live?, startedAt?, liveTail?, detail? }) — the SAME shape the real
// stream feeds <ActivityLedger>. `detail.node` is the borderless block that
// opens inside the unified card.
const READ_ROWS = [
  { id: "r1", tool: "read", arg: { text: "pkg/serve/ws.go" }, out: "232 lines", status: "ok" },
  { id: "r2", tool: "grep", arg: { text: '"Subscribe(" in pkg/bus' }, out: "7 matches", status: "ok" },
];

// A live batch: some done rows + a trailing live bash streaming its tail.
const TAIL_ROWS = [
  { id: "tr1", tool: "read", arg: { text: "pkg/serve/ws.go" }, out: "130 lines", status: "ok" },
  { id: "tr2", tool: "grep", arg: { text: '"Subscribe("' }, out: "7 matches", status: "ok" },
  { id: "tr3", tool: "bash", arg: { text: "go vet ./pkg/serve/" }, out: "exit 1", status: "err" },
  { id: "tr4", tool: "edit", arg: { text: "pkg/serve/ws.go" }, out: "+5 −3", status: "ok" },
  {
    id: "tr5",
    tool: "bash",
    arg: { text: "go test -race -count=50 ./pkg/serve/" },
    live: true,
    startedAt: Date.now() - 7000,
    liveTail:
      "=== RUN   TestResumeDelivery\n--- PASS: TestResumeDelivery (0.41s)\n=== RUN   TestResumeSnapshot",
  },
];

// A finished batch with an inline diff + bash output, details fused as nodes.
const WORK_ROWS = [
  { id: "w1", tool: "read", arg: { text: "pkg/serve/ws.go" }, out: "340 lines", status: "ok" },
  {
    id: "w2",
    tool: "edit",
    arg: { text: "pkg/serve/ws.go · resume()" },
    out: "+5 −3",
    status: "ok",
    detail: { node: <DiffBlock className="flush" diffText={EDIT_DIFF} filename="pkg/serve/ws.go" /> },
  },
  {
    id: "w3",
    tool: "bash",
    arg: { text: "go test -race -count=50 ./pkg/serve/" },
    out: "3 lines",
    status: "ok",
    detail: { node: <CodeBlock className="flush" code={BASH_OUTPUT} lang="bash" showHeader={false} /> },
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
        {hasTokens && <span class="tokens"><TokenFlow up={tokensUp} down={tokensDown} variant="compact" /></span>}
        <PermissionControl mode={perm} sheet onChange={() => {}} />
        <button type="button" class="spend spend-btn" aria-label="Show usage" title="Estimated session cost">
          ~{spend}
        </button>
      </div>
    </div>
  );
}

// SUBAGENT SPECIMENS — the real MobileSubagentView (5J) mounted with mock
// store-shaped sessions, mirroring the desktop SubagentGallery so the mobile
// fork view is reviewable at ?view=mobile too. Each is a { session, jobId }
// pair; onBack is a no-op (no live store to clear here). The steer Composer it
// wraps is store-bound, so it renders empty but faithful.
const SUBAGENT_RUNNING = {
  session: {
    id: "sess-live",
    title: "release 0.11",
    messages: [],
    subagents: {
      changelog: {
        jobId: "changelog",
        task: "Collect the PRs merged since v0.10.0, group them by area (serve, tui, providers), and draft changelog entries in the house style. Skip dependabot noise.",
        model: "terra",
        async: false,
        status: "running",
        usage: { inputTokens: 14200, outputTokens: 4100, costUSD: 0.031 },
        messages: [
          { _type: "tool_start", tool_call_id: "t2", tool_name: "grep", args: { pattern: "Merge pull request" }, status: "ok", result: "23 matches" },
          { _type: "tool_start", tool_call_id: "t3", tool_name: "read", args: { path: "CHANGELOG.md" }, status: "ok", result: "88 lines" },
          { role: "assistant", content: "Grouping so far: 9 in serve (5 frontend), 6 in providers, 4 in tui. Drafting the serve section while the last PR bodies come in." },
          { _type: "tool_start", tool_call_id: "t4", tool_name: "bash", args: { cmd: "gh pr view 412 --json title,labels,body" }, status: "running" },
        ],
      },
      docs: {
        jobId: "docs", task: "Rewrite docs/serve.md security section", model: "sonnet",
        async: false, status: "running",
        messages: [{ _type: "tool_start", tool_call_id: "d1", tool_name: "read", args: { path: "docs/serve.md" }, status: "running" }],
      },
      tests: {
        jobId: "tests", task: "Run the full test sweep with -race", model: "sonnet",
        async: false, status: "running",
        messages: [{ _type: "tool_start", tool_call_id: "x1", tool_name: "bash", args: { cmd: "go test -race ./..." }, status: "running" }],
      },
    },
  },
  jobId: "changelog",
};

const SUBAGENT_COMPLETED = {
  session: {
    id: "sess-done",
    title: "release 0.11",
    messages: [],
    subagents: {
      tests: {
        jobId: "tests",
        task: "Run the full test sweep with -race across all packages. Report any failure with enough context to fix it; note skips and why.",
        model: "sonnet",
        async: false,
        status: "completed",
        usage: { inputTokens: 31200, outputTokens: 4800, costUSD: 0.041 },
        result: "full sweep green, -race clean, 2 skips (docker)",
        messages: [
          { _type: "tool_start", tool_call_id: "c1", tool_name: "bash", args: { cmd: "go test -race ./..." }, status: "ok", result: "ok  full sweep\n412 tests" },
          { _type: "tool_start", tool_call_id: "c2", tool_name: "bash", args: { cmd: "go vet ./..." }, status: "ok", result: "clean" },
          { role: "assistant", content: "Full sweep green. 412 tests across 47 packages, -race clean, 2 skips (both docker-gated in pkg/sandbox). Nothing blocks the release." },
        ],
      },
    },
  },
  jobId: "tests",
};

// MobileSubagentSpecimen — the fork view inside the phone frame. It's a
// full-screen surface, so it sits alone in a .mconv container (like the real
// screen when session.viewingSubagent is set).
function MobileSubagentSpecimen({ spec }) {
  return (
    <div class="mconv">
      <MobileSubagentView session={spec.session} jobId={spec.jobId} onBack={noop} />
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

          <ActivityLedger rows={READ_ROWS} visibleDone={1} />

          <ActivityLedger rows={WORK_ROWS} visibleDone={1} />

          <ActivityLedger rows={TAIL_ROWS} visibleDone={1} />

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

      <MobileComposerSpecimen status="running tests" perm="yolo" spend="$1.84" tokensUp={41200} tokensDown={8700} />
    </div>
  );
}

// DrawerSpecimen — the conversation specimen with the sessions drawer forced
// open on top of it, so the gallery can show the dropdown statically.
function DrawerSpecimen() {
  return (
    <>
      <MobileConversationSpecimen />
      <SessionDrawer
        open
        onClose={noop}
        active={DRAWER_SESSIONS.filter((s) => !s.saved)}
        saved={DRAWER_SESSIONS.filter((s) => s.saved)}
        activeCount={4}
        savedCount={2}
        projects={[{ cwd: "/home/dev/moa" }, { cwd: "/home/dev/moa/pulse-api" }]}
        onSelect={noop}
        onCreate={noop}
        onSettings={noop}
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

        <figure class="mgal-figure">
          <div class="mgal-device">
            <span class="mgal-notch" aria-hidden="true" />
            <div class="mgal-screen">
              <MobileSubagentSpecimen spec={SUBAGENT_RUNNING} />
            </div>
          </div>
          <figcaption class="mgal-caption">
            Subagent — running (fork identity · sibling rail · steer)
          </figcaption>
        </figure>

        <figure class="mgal-figure">
          <div class="mgal-device">
            <span class="mgal-notch" aria-hidden="true" />
            <div class="mgal-screen">
              <MobileSubagentSpecimen spec={SUBAGENT_COMPLETED} />
            </div>
          </div>
          <figcaption class="mgal-caption">
            Subagent — completed (outcome banner)
          </figcaption>
        </figure>
      </div>
    </div>
  );
}
