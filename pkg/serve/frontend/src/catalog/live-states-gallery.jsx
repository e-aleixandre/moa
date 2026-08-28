import {
  UserWaypoint,
  AssistantDocument,
  DelegationBlock,
  StreamingSkeleton,
  PermissionCard,
} from "../components/index.js";
import { Composer, StatusStrip, Pane, GridToolbar, LiveDock } from "../layout/index.js";
import { StateDot } from "../primitives/index.js";
import "./live-states-gallery.css";

// LiveStatesGallery — specimens for the current live conversation surfaces.

// Live Dock specimen — same descriptor shape as liveTrayAgents(session).
const LIVE_DOCK_AGENTS = [
  { id: "changelog", kind: "subagent", name: "changelog", accent: "sky", action: "gh pr view 412 · reading labels", time: "1m 12s" },
  { id: "tests", kind: "subagent", name: "tests", accent: "teal", action: "auditing flaky specs", time: "2m 04s" },
  { id: "bench", kind: "bash", name: "bash", action: "go test -race ./...", time: "4m 18s" },
];

// DelegationBlock specimens:
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


// --- Section 1: delegation block -----------------------------------------

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
        <LiveDock agents={LIVE_DOCK_AGENTS} onOpen={() => {}} />
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

// --- Section 2: grid alive -----------------------------------------------

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

      <DelegationSection />
      <LiveDockSection />
      <GridAliveSection />
    </div>
  );
}
