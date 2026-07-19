import { SubagentView } from "../layout/SubagentView/SubagentView.jsx";
import "./live-states-gallery.css";

// SubagentGallery — static specimens of the real SubagentView (5J), mounted with
// mock store-shaped sessions so the implemented view (not the mockup) can be
// eyeballed at ?view=subagent. Each specimen is a { session, jobId } pair that
// exercises one state: running with siblings, thinking (lone), completed, failed.
// onBack is a no-op here (there is no live store to clear).

const noop = () => {};

// A running subagent mid-tool, part of a 3-way fanout (sibling rail shows).
const RUNNING = {
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
        usage: { inputTokens: 14200, outputTokens: 4100, costUSD: 0.031, elapsedMs: 72000 },
        messages: [
          { _type: "tool_start", tool_call_id: "t1", tool_name: "bash", args: { cmd: "git log v0.10.0..HEAD --merges --oneline" }, status: "ok", result: "23 merges" },
          { _type: "tool_start", tool_call_id: "t2", tool_name: "grep", args: { pattern: "Merge pull request" }, status: "ok", result: "23 matches" },
          { _type: "tool_start", tool_call_id: "t3", tool_name: "read", args: { path: "CHANGELOG.md" }, status: "ok", result: "88 lines\nline\nline" },
          { role: "assistant", content: "Grouping so far: 9 in serve (5 frontend), 6 in providers, 4 in tui, 4 misc. Drafting the serve section while the last PR bodies come in." },
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

// A completed subagent (lone → no sibling rail, outcome banner green).
const COMPLETED = {
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
        usage: { inputTokens: 31200, outputTokens: 4800, costUSD: 0.041, elapsedMs: 161000 },
        result: "full sweep green, -race clean, 2 skips (docker)",
        messages: [
          { _type: "tool_start", tool_call_id: "c1", tool_name: "bash", args: { cmd: "go test -race ./..." }, status: "ok", result: "ok  full sweep\n412 tests" },
          { _type: "tool_start", tool_call_id: "c2", tool_name: "bash", args: { cmd: "go vet ./..." }, status: "ok", result: "clean" },
          { role: "assistant", content: "Full sweep green. 412 tests across 47 packages, -race clean, 2 skips (both docker-gated in pkg/sandbox). go vet has no findings. Nothing blocks the release." },
        ],
      },
    },
  },
  jobId: "tests",
};

// A failed subagent (outcome banner red, real error extracted).
const FAILED = {
  session: {
    id: "sess-fail",
    title: "migrate sqlite",
    messages: [],
    subagents: {
      audit: {
        jobId: "audit",
        task: "Audit every table in state.db for columns unused since v0.8 and propose the drop migration.",
        model: "terra",
        async: false,
        status: "failed",
        usage: { inputTokens: 12100, outputTokens: 1900, costUSD: 0.018, elapsedMs: 63000 },
        messages: [
          { _type: "tool_start", tool_call_id: "f1", tool_name: "read", args: { path: "pkg/session/schema.go" }, status: "ok", result: "164 lines" },
          { _type: "tool_start", tool_call_id: "f2", tool_name: "bash", args: { cmd: 'sqlite3 state.db ".schema"' }, status: "error", result: "Error: database is locked\nretry 2/3 after 2s… retry 3/3 after 4s…\nError: database is locked (SQLITE_BUSY)" },
        ],
      },
    },
  },
  jobId: "audit",
};

function Specimen({ title, alt, spec }) {
  return (
    <section class="lsg-section">
      <h2>
        {title} <span class="alt">{alt}</span>
      </h2>
      <div class="lsg-convo sa-gallery-frame">
        <SubagentView session={spec.session} jobId={spec.jobId} onBack={noop} />
      </div>
    </section>
  );
}

export function SubagentGallery() {
  return (
    <div class="lsg">
      <header class="lsg-head">
        <h1>moa · Studio — <em>subagent view</em></h1>
        <p>
          The real implemented SubagentView (5J), mounted with mock sessions. Zoom into one fork:
          same Stream as the parent, framed by a thin accent thread, a breadcrumb, a sibling rail,
          a fused now-line and a terminal outcome banner.
        </p>
      </header>
      <Specimen title="Running · tool in flight" alt="3-way fanout · sibling rail · steer composer" spec={RUNNING} />
      <Specimen title="Completed" alt="lone subagent · outcome banner green · thread turns green" spec={COMPLETED} />
      <Specimen title="Failed" alt="real error extracted · outcome banner red" spec={FAILED} />
    </div>
  );
}
