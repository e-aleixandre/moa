import { GitFork } from "lucide-preact";
import {
  UserWaypoint,
  AssistantDocument,
  FanoutBlock,
  DelegationBlock,
  BackgroundJob,
  StreamingSkeleton,
  TypingDots,
  ToolTicker,
  PermissionCard,
} from "../components/index.js";
import { AgentTray, Composer, StatusStrip, Pane, GridToolbar, LiveDock } from "../layout/index.js";
import { StateDot } from "../primitives/index.js";
import "./live-states-gallery.css";

// LiveStatesGallery — reproduces the 3 sections of live-states.html: parallel
// subagent fan-out, a background job with a collapsible log tail,
// and the grid with three live panes at once. Everything reuses existing
// primitives and components (Pane, AgentTray, AssistantDocument, ...); no
// new layout is invented beyond what the phase explicitly requires
// (FanoutBlock, BackgroundJob, StreamingSkeleton/TypingDots, ToolTicker).

const FANOUT_AGENTS = [
  {
    id: "changelog",
    name: "changelog",
    accent: "sky",
    state: "running",
    action: "grep merged PRs since v0.10.0 · 23 found",
    time: "1m 12s",
  },
  {
    id: "docs",
    name: "docs",
    accent: "teal",
    state: "running",
    action: "rewriting docs/serve.md §security",
    time: "0m 47s",
  },
  {
    id: "tests",
    name: "tests",
    state: "done",
    result: "ok · 412 tests",
    resultDesc: "full sweep green, -race clean, 2 skips (docker)",
  },
];

const TRAY_AGENTS = [
  { id: "changelog", kind: "subagent", name: "changelog", accent: "sky", action: "scanning PRs", time: "1m 12s" },
  { id: "docs", kind: "subagent", name: "docs", accent: "teal", action: "serve.md", time: "0m 47s" },
  { id: "tests", kind: "bash", name: "bash", action: "go test ./...", time: "0m 09s" },
];

// Live Dock specimen — same descriptor shape as liveTrayAgents(session).
const LIVE_DOCK_AGENTS = [
  { id: "changelog", kind: "subagent", name: "changelog", accent: "sky", action: "gh pr view 412 · reading labels", time: "1m 12s" },
  { id: "tests", kind: "subagent", name: "tests", accent: "teal", action: "auditing flaky specs", time: "2m 04s" },
  { id: "bench", kind: "bash", name: "bash", action: "go test -race ./...", time: "4m 18s" },
];

// DelegationBlock specimens (replaces FanoutBlock — see DelegationSection):
// one live wave (2 running + 1 done, unsettled) and one fully terminal wave
// that starts collapsed (settled, 2 done + 1 failed).
const DELEGATION_LIVE_AGENTS = [
  {
    id: "changelog",
    name: "changelog",
    accent: "sky",
    state: "running",
    action: "gh pr view 412 · reading labels",
    time: "1m12s",
    bashJobs: [],
  },
  {
    id: "tests",
    name: "tests",
    accent: "teal",
    state: "running",
    action: "waiting on bash job",
    time: "2m04s",
    bashJobs: [],
  },
  {
    id: "docs",
    name: "docs",
    accent: "mauve",
    state: "done",
    chip: "security section rewritten · 3 files",
    time: "1m41s",
    bashJobs: [],
  },
];

const DELEGATION_SETTLED_AGENTS = [
  {
    id: "changelog",
    name: "changelog",
    accent: "sky",
    state: "done",
    chip: "23 PRs grouped · draft ready",
    time: "3m58s",
    bashJobs: [],
  },
  {
    id: "docs",
    name: "docs",
    accent: "mauve",
    state: "done",
    chip: "security section rewritten",
    time: "1m41s",
    bashJobs: [],
  },
  {
    id: "tests",
    name: "tests",
    accent: "teal",
    state: "failed",
    chip: "SQLITE_BUSY · db locked",
    time: "4m02s",
    bashJobs: [],
  },
];


const BGJOB_LINES = [
  { text: "ok  pkg/bus      0.31s", tone: "dim" },
  { text: "ok  pkg/session  1.24s", tone: "dim" },
  { text: "ok  pkg/usage    0.09s", tone: "ok" },
  { text: "=== RUN  TestServeResume/reconnect_mid_stream" },
];

const TICKER_LINES = [
  { tool: "read", text: "docs/serve.md · §security" },
  { tool: "grep", text: '"merged:" · 23 PRs' },
  { tool: "edit", text: "CHANGELOG.md · drafting" },
];

// --- Section 1: parallel subagents ------------------------------------

function FanoutSection() {
  return (
    <section class="lsg-section">
      <h2>
        Parallel subagents <span class="alt">fan-out block in the stream · running → done → result</span>
      </h2>

      <div class="lsg-convo">
        <div class="lsg-convo-head">
          <StateDot state="running" size={9} />
          <span class="title">release 0.11</span>
          <span class="path">~/dev/moa/main</span>
          <span class="mp">sol ▰▰▰▱</span>
        </div>
        <div class="lsg-convo-body">
          <UserWaypoint time="10:41">
            <p>
              Prep the 0.11 release: changelog from the merged PRs, docs pass, and a full test sweep.
              Parallelize it.
            </p>
          </UserWaypoint>

          <AssistantDocument>
            <p>
              Fanning out — three subagents, one per track. I'll assemble the release notes when they
              report back.
            </p>
          </AssistantDocument>

          <FanoutBlock
            task="release prep"
            count={3}
            startedAt="10:41"
            agents={FANOUT_AGENTS}
            onViewReport={() => {}}
          />

          <AssistantDocument>
            <p>
              Tests are already green. When <code>changelog</code> lands I'll draft the notes against it{" "}
              <TypingDots />
            </p>
          </AssistantDocument>
        </div>

        <AgentTray agents={TRAY_AGENTS} />
        {/* Composer specimen: no session, so it shows the idle default (its
            queue/activity are driven by a real session in the app). */}
        <Composer />
        <StatusStrip task="2 agents running · 1 result waiting" ctxPercent={44} tokensUp={58300} tokensDown={12100} />
      </div>

      <p class="lsg-caption">
        <b>Running rows</b> show the agent's current action as a live mono line (blinking cursor) over an
        indeterminate bar — glance and know what each one is doing. <b>Finished rows</b> turn green,
        compress into a result chip, and offer "view report →". The tray mirrors the same jobs when you
        scroll away; a finished-but-unread result gets the peach unseen pulse. While all this runs, the
        composer stays yours — typing is never blocked.
      </p>
    </section>
  );
}

// --- Section 1b: delegation block (replaces the fan-out block above) -----

function DelegationSection() {
  return (
    <section class="lsg-section">
      <h2>
        Delegation block <span class="alt">one block per wave · live rows mutate in place · auto-collapse when settled</span>
      </h2>

      <div class="lsg-convo">
        <div class="lsg-convo-head">
          <StateDot state="running" size={9} />
          <span class="title">release 0.11</span>
          <span class="path">~/dev/moa/main</span>
          <span class="mp">sol ▰▰▰▱</span>
        </div>
        <div class="lsg-convo-body">
          <UserWaypoint time="10:41">
            <p>Prep the 0.11 release: changelog, docs pass, full test sweep. Parallelize it.</p>
          </UserWaypoint>

          <AssistantDocument>
            <p>Fanning out — three subagents, one per track.</p>
          </AssistantDocument>

          <DelegationBlock
            agents={DELEGATION_LIVE_AGENTS}
            summary={{ total: 3, done: 1, failed: 0 }}
            settled={false}
          />
        </div>
      </div>

      <p class="lsg-caption">
        <b>Live wave</b> (unsettled): header + one row per agent, running rows breathe with the
        indeterminate bar under their action, done rows fold to a green check + result chip, and the
        sky hairline along the bottom edge signals the whole block still has life.
      </p>

      <div class="lsg-convo">
        <div class="lsg-convo-head">
          <StateDot state="idle" size={9} />
          <span class="title">release 0.11</span>
          <span class="path">~/dev/moa/main</span>
        </div>
        <div class="lsg-convo-body">
          <AssistantDocument>
            <p>All three tracks reported back — tests failed on a locked db, the rest is ready.</p>
          </AssistantDocument>

          <DelegationBlock
            agents={DELEGATION_SETTLED_AGENTS}
            summary={{ total: 3, done: 2, failed: 1 }}
            settled
          />
        </div>
      </div>

      <p class="lsg-caption">
        <b>Settled wave</b>: starts collapsed to the header line (<code>⑂ 3 agents · 2 ✓ · 1 ✗</code>) —
        a tap re-expands the rows for a post-mortem look. No sweep, gray border: it's history now.
      </p>
    </section>
  );
}

// --- Section 1c: Live Dock (persistent mirror above the composer) --------

function LiveDockSection() {
  return (
    <section class="lsg-section">
      <h2>
        Live Dock <span class="alt">async never lost · compact bar ⇄ expanded panel · shown only when the block is off-screen</span>
      </h2>

      <div class="lsg-dock-frame">
        <span class="lsg-dock-tag">compact (spotlight rotates every 4s)</span>
        <LiveDock agents={LIVE_DOCK_AGENTS} onOpen={() => {}} onJump={() => {}} />
      </div>

      <p class="lsg-caption">
        <b>Live Dock</b> is the delegation block peeking above the composer once you've scrolled its
        inline surface out of view. Identity dots + count on the left, a rotating spotlight of what one
        live thing is doing in the middle; tap to expand into one row per live agent/bash (same visual
        language as the block), each with a <code>↑</code> jump back to its point in the stream. It only
        exists while something is alive AND off-screen — scroll back to the block and it retracts.
      </p>
    </section>
  );
}

// --- Section 2: background jobs -----------------------------------------

function BackgroundJobSection() {
  return (
    <section class="lsg-section">
      <h2>
        Background jobs <span class="alt">long bash in async · live tail · you keep working</span>
      </h2>

      <div class="lsg-convo">
        <div class="lsg-convo-head">
          <StateDot state="running" size={9} />
          <span class="title">ws race fix</span>
          <span class="path">~/dev/moa/main</span>
          <span class="mp">sol ▰▰▰▱</span>
        </div>
        <div class="lsg-convo-body">
          <AssistantDocument>
            <p>
              Full suite takes ~4 min, so I've launched it <strong>in the background</strong> and I'll
              keep going with the changelog meanwhile:
            </p>
          </AssistantDocument>

          <BackgroundJob
            jobLabel="BG · JOB 2"
            cmd="go test -race ./..."
            progress="pkg 31/47"
            elapsed="2:18"
            lines={BGJOB_LINES}
            defaultOpen
          />

          <AssistantDocument streaming>
            <p>
              Meanwhile, drafting the changelog entry for the reconnect fix. The race was subtle: the
              resume snapshot and the live subscription didn't overlap, so any event published in that
            </p>
          </AssistantDocument>
          <StreamingSkeleton />
        </div>

        <Composer />
        <StatusStrip task="streaming · bg job 2:18" ctxPercent={62} tokensUp={41200} tokensDown={8700} />
      </div>

      <p class="lsg-caption">
        <b>The strip</b> pins the job with elapsed time and a progress tail (<code>pkg 31/47</code>);
        "peek" unfolds the last lines of live output — the newest line carries the teal cursor. Above it,
        the assistant keeps <b>streaming its own text</b> (mauve caret + shimmer skeleton for the
        not-yet-arrived paragraph). Two things happening at once, each with its own pulse, neither
        shouting.
      </p>
    </section>
  );
}

// --- Section 3: grid alive -----------------------------------------------

function GridAliveSection() {
  return (
    <section class="lsg-section">
      <h2>
        The grid, alive <span class="alt">three sessions working at once · attention lamp on</span>
      </h2>

      <div class="lsg-gridmock">
        <GridToolbar paneCount={3} preset="p3" needsYouCount={1} />
        <div class="lsg-gm-grid">
          <Pane
            variant="tall"
            focused
            title="ws race fix"
            state="running"
            hideComposer
            footer={
              <>
                <span class="pulse-b">● streaming</span>
                <span>bg job 2:18</span>
                <span class="spacer">ctx 62%</span>
              </>
            }
          >
            <p>Drafting release notes…</p>
            <StreamingSkeleton widths={["94%", "81%", "56%"]} />
          </Pane>

          <Pane
            title="release 0.11"
            state="running"
            hideComposer
            footer={
              <>
                <span class="pulse-b">● working</span>
                <span class="spacer">1m 40s</span>
              </>
            }
          >
            <span class="lsg-ticker-head">
              <GitFork size={12} aria-hidden="true" /> 2 subagents running
            </span>
            <ToolTicker lines={TICKER_LINES} />
          </Pane>

          <Pane
            title="deploy pulse api"
            state="permission"
            titleTone="yellow"
            hideComposer
            footer={
              <span class="pulse-y">
                <StateDot state="permission" size={7} /> waiting 0:42
              </span>
            }
          >
            <p>Build green, unit staged.</p>
            <PermissionCard title="moa wants to run" command="systemctl --user restart pulse-api" />
          </Pane>
        </div>
      </div>

      <p class="lsg-caption">
        <b>Each pane keeps its own pulse</b>: P1 streams (shimmer lines + blue footer), P2 shows a rotating
        ticker of its latest tool calls — enough to feel the work without reading it — and P3 breathes
        yellow with an inline permission card. The lamp in the toolbar aggregates: one click focuses the
        pane that needs you. Blue things breathe slow (1.8s); yellow breathes faster (1.1s) — urgency has
        a tempo.
      </p>
    </section>
  );
}

export function LiveStatesGallery() {
  return (
    <div class="lsg">
      <header class="lsg-head">
        <h1>
          moa studio · <em>live states</em>
        </h1>
        <p>
          How the app feels when it's actually doing things: parallel subagents, background jobs,
          streaming responses, and a grid where three sessions work at once. Everything here breathes on
          its own.
        </p>
      </header>

      <FanoutSection />
      <DelegationSection />
      <LiveDockSection />
      <BackgroundJobSection />
      <GridAliveSection />
    </div>
  );
}
