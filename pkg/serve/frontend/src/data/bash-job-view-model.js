// bash-job-view-model.js — PURE projection for the BashJobView, the read-only
// counterpart of subagent-view-model.js. Given the store's `session` and the
// job_id being viewed, it derives everything the view paints (command, cwd,
// status, outcome, output lines) WITHOUT any preact/DOM, so it is testable in
// the DOM-less bun runner.
//
// It reads the SAME store shape ws-handlers writes for a root background bash:
// session.subagents[jobId] = { kind:'bash', task:<command>, cwd, status,
// messages:[ one tool_start row ] }, whose `streamingResult` grows with the
// live deltas and whose `result` holds the final output. Reading that single
// source is what makes the view reconnect-safe for free: the init snapshot
// rebuilds the very same entry from `bash_jobs` (accumulated output included).

const TERMINAL_STATUSES = new Set(['completed', 'failed', 'cancelled', 'error', 'done']);

// MAX_LINES bounds how much of a long-running job's output is handed to the
// view. A build/test job can emit tens of thousands of lines and rendering all
// of them as DOM nodes on every delta would stall the pane; the tail is what a
// live log is read for, so older lines are dropped and merely counted.
export const MAX_LINES = 1000;

// bashOutput returns the output accumulated so far: the streaming buffer while
// the job runs (deltas land there), the final result once it ended. Both are
// read because bash_job_end swaps one for the other in a single store write.
function bashOutput(job) {
  const row = (Array.isArray(job.messages) ? job.messages : [])
    .find((m) => m && m._type === 'tool_start' && m.tool_name === 'bash');
  if (!row) return '';
  return String(row.streamingResult || row.result || '');
}

// outcomeOf maps a terminal status to the outcome banner variant, mirroring
// subagent-view-model's terminalOutcome so both detail views name the same
// three ends with the same words/colors.
function outcomeOf(status) {
  if (status === 'failed' || status === 'error') return 'failed';
  if (status === 'cancelled') return 'cancelled';
  return 'completed';
}

// bashJobView is the single entry point. Returns null when the viewed job is
// no longer in the session (the "rebound": the caller clears viewingBashJob and
// falls back to the parent), exactly like subagentView. Otherwise:
//
//   {
//     jobId, command, cwd, status,
//     terminal:boolean, outcome:'completed'|'failed'|'cancelled'|null,
//     canCancel:boolean, cancelling:boolean,
//     output:string, lines:[string], hiddenLines:number,
//   }
export function bashJobView(session, jobId) {
  if (!session || !jobId) return null;
  const jobs = normalizeJobs(session.subagents);
  const job = jobs[jobId];
  // Only a root background job has a detail view. A bash launched BY a
  // subagent lives as a tool row inside that subagent's transcript (see
  // attachBashJob), so it is never addressable here — guard against a stale
  // viewingBashJob pointing at a subagent id.
  if (!job || job.kind !== 'bash' || job.ownerAgentId) return null;

  const status = job.status || 'running';
  const terminal = TERMINAL_STATUSES.has(status);
  const output = bashOutput(job);
  // A trailing newline would otherwise render as a phantom empty last line
  // under the live cursor.
  const all = output ? output.replace(/\n$/, '').split('\n') : [];
  const hiddenLines = Math.max(0, all.length - MAX_LINES);

  return {
    jobId,
    command: job.task || '',
    cwd: job.cwd || '',
    status,
    terminal,
    outcome: terminal ? outcomeOf(status) : null,
    // 'cancelling' means the stop was already requested and the process is
    // winding down: still live (output can keep arriving) but no longer
    // stoppable, so the button reports the pending stop instead of re-arming.
    canCancel: status === 'running',
    cancelling: status === 'cancelling',
    output,
    lines: hiddenLines > 0 ? all.slice(hiddenLines) : all,
    hiddenLines,
  };
}

// normalizeJobs accepts session.subagents as a map keyed by job_id (the common
// shape) or an array of records, mirroring subagent-view-model's
// normalizeSubagents — background jobs share that same container.
function normalizeJobs(subagents) {
  if (!subagents) return {};
  if (!Array.isArray(subagents)) return subagents;
  const out = {};
  for (const s of subagents) {
    if (s && s.jobId) out[s.jobId] = s;
  }
  return out;
}
