import { GitFork } from "lucide-preact";
import {
  UserWaypoint,
  AssistantDocument,
  FanoutBlock,
  BackgroundJob,
  StreamingSkeleton,
  TypingDots,
  ToolTicker,
  PermissionCard,
} from "../components/index.js";
import { AgentTray, Composer, StatusStrip, Pane, GridToolbar } from "../layout/index.js";
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
  { key: "changelog", who: "changelog", color: "sky", what: "scanning PRs", time: "1m 12s" },
  { key: "docs", who: "docs", color: "teal", what: "serve.md", time: "0m 47s" },
  { key: "tests", who: "tests", state: "done", what: "report ready" },
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
        <StatusStrip task="2 agents running · 1 result waiting" ctxPercent={44} tokensUp="58.3k" tokensDown="12.1k" />
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
        <StatusStrip task="streaming · bg job 2:18" ctxPercent={62} tokensUp="41.2k" tokensDown="8.7k" />
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
      <BackgroundJobSection />
      <GridAliveSection />
    </div>
  );
}
