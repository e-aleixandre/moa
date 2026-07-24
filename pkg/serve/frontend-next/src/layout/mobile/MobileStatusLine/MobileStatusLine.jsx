import { useState, useEffect } from "preact/hooks";
import { Check } from "lucide-preact";
import { ModelSelector, ModelPill, TokenFlow } from "../../../components/index.js";
import { api } from "../../../data/api.js";
import { statusStripModel } from "../../../data/util/status-strip-model.js";
import {
  usageForSession,
  usageLevel,
  fmtReset,
  fmtCost,
  money,
} from "../../../data/util/usage-pills.js";
import { deriveModelSpecs, matchSelectedModel, modelAccent } from "../../../data/selectors.js";
import { configureSession } from "../../../data/session-actions.js";
import { addToast } from "../../../data/notifications.js";
import { modelCodename, shortModel, fmtTokens } from "../../../data/util/format.js";
import { MobileSheet } from "../MobileSheet/MobileSheet.jsx";
import "./MobileStatusLine.css";

// MobileStatusLine — the persistent mobile chrome. One line pinned under the
// composer holding three single-scope doors, each its own hit area and
// destination, each opening the approved bottom sheet (MobileSheet) that STOPS
// above the composer — never the centered generic <Sheet> modal — plus one
// read-only telemetry at the far right:
//
//   1. context ring + cost (LEFT) — the canonical teal SVG ring + pct + the
//      estimated session cost (~, spend severity). Tap → "Context & usage":
//      session cost · context · plan windows · extra, and the auto-compaction
//      limit — the one setting measured on the very ring this door wears.
//   2. ModelPill — current model + thinking. Tap → "Model & thinking": the real
//      ModelSelector, and nothing else. It used to also carry two unbuilt
//      session-settings rows and Rewind, which made it the sheet you opened for
//      anything and found nothing in; the limit moved to door 1 (its scope) and
//      Rewind onto the waypoints themselves (UserWaypoint), where "rewind to
//      WHERE" is answered by the tap instead of by a second screen.
//   3. permission chip — the glanceable safety color AND the door. ONE tap
//      reveals the complete YOLO/AUTO/ASK choice directly in the sheet (no
//      intermediate chip, no second tap).
//   4. TokenFlow (RIGHT) — the per-run ↑/↓ heartbeat. NOT a door: it is the
//      same shared component the desktop StatusStrip carries in the same
//      corner, so "is it still chewing?" is answered identically on both.
//
// Sessions is deliberately NOT here: the door to the session list is the
// floating title chip at the top of the screen (MobileTitleChip), which also
// carries the cross-session attention dot. One door per destination, and the
// one that switches session belongs next to the session's own name.
//
// Every destination lays out real shared data (usageForSession / ModelSelector /
// the canonical MODES / configureSession) in the mock's visual structure. Global
// settings (notifications) live behind the SessionDrawer footer, not here.

// Canonical permission modes — same copy/order as the shared PermissionControl.
const PERM_MODES = [
  { value: "yolo", label: "YOLO", desc: "Run everything — never ask" },
  { value: "auto", label: "AUTO", desc: "Ask only for risky commands" },
  { value: "ask", label: "ASK", desc: "Ask before every command" },
];

// Granularity of the context-limit slider, in percentage points. 5 rather than
// 10 because the honest answer to "where should this compact?" is a number, not
// a menu — and a phone can't offer a number without a keyboard. 5 gets close
// enough to freehand that the difference stops mattering, while every stop is
// still a comfortable thumb width apart.
const LIMIT_STEP = 5;

// The limit is expressed in percent, not tokens, because the ring right above
// reads in percent against the SAME denominator (the server computes
// context_percent from model.MaxInput, never from the limit) — so "compact at
// 70%" is literally "compact when that ring reaches 70". Tokens would be a
// second unit for one fact, and would need the window known by heart.
const pctToTokens = (pct, win) => Math.round((win * pct) / 100);
const tokensToPct = (tokens, win) => Math.round((tokens * 100) / win);

// ContextLimitRow — the session's auto-compaction threshold. Sets CompactAt on
// the agent (pkg/agent/agent.go), which caps the effective window so compaction
// fires early instead of at the brim. Hidden when the window is unknown (an
// unrecognized model), and locked while the session is busy: SetCompactAt
// reconfigures the agent and the server refuses it mid-run with a 409.
//
// One slider, and 100% IS "auto" — not a separate chip beside it. That is not a
// label trick: core.EffectiveWindow clamps any threshold at or above the window
// back to the window, so the far right of this track is exactly the behavior a
// session has with no limit set. Ending the scale there keeps the control to one
// axis with one unit, and reads the way the setting actually works — the further
// left you drag, the earlier it summarizes.
function ContextLimitRow({ session, disabled }) {
  const win = session.contextWindow || 0;
  const compactAt = session.compactAt || 0;
  // The server's own floor (ReserveTokens + KeepRecent + tail margin): below it
  // the engine raises the threshold, so offering lower would promise a
  // compaction point it will not honor. Ceiling twice — floor→percent, then
  // percent→step — because rounding either one down would put the lowest
  // reachable stop back under the floor: on a 200k window the floor is 20.19%,
  // and a rounded 20% would be 384 tokens short of it. So 25%, not 20%.
  const minPct = Math.min(
    100 - LIMIT_STEP,
    Math.ceil(Math.ceil(((session.compactAtMin || 0) * 100) / (win || 1)) / LIMIT_STEP) *
      LIMIT_STEP,
  );
  const settledPct = compactAt > 0 ? tokensToPct(compactAt, win) : 100;

  // Local while dragging so the readout tracks the thumb; the store only hears
  // about it on release (onChange), since each commit reconfigures the agent.
  const [dragPct, setDragPct] = useState(null);
  useEffect(() => setDragPct(null), [compactAt, win]);
  if (!win) return null;
  const pct = dragPct ?? settledPct;

  const commit = (next) => {
    setDragPct(next);
    const tokens = next >= 100 ? 0 : pctToTokens(next, win);
    if (tokens === compactAt) return;
    configureSession(session.id, { compactAt: tokens }).catch((e) => {
      setDragPct(null);
      addToast({
        title: "Could not set the context limit",
        detail: String(e.message || e),
        type: "error",
      });
    });
  };

  return (
    <div class="msl-ugroup">
      <div class="msl-urow msl-limit">
        <span class="uk">Compact at</span>
        <span class="msl-limit-val">
          {pct >= 100 ? "auto" : `${pct}%`}
          {pct < 100 && (
            <span class="msl-limit-tok">{fmtTokens(pctToTokens(pct, win))}</span>
          )}
        </span>
      </div>
      <input
        type="range"
        class="msl-limit-slider"
        min={minPct}
        max={100}
        step={LIMIT_STEP}
        value={pct}
        disabled={disabled}
        style={{ "--fill": `${((pct - minPct) * 100) / (100 - minPct)}%` }}
        aria-label="Compact at"
        aria-valuetext={pct >= 100 ? "auto" : `${pct} percent`}
        onInput={(e) => setDragPct(Number(e.currentTarget.value))}
        onChange={(e) => commit(Number(e.currentTarget.value))}
      />
      <span class="msl-limit-note">
        {pct < 100
          ? `Summarizes and keeps going once the ring hits ${pct}%.`
          : "Summarizes only when the model's window is nearly full."}
      </span>
    </div>
  );
}

// UsageSheetBody — the approved "Context & usage" presentation (fx-shared
// FX_USAGE_BODY): a session group (cost · context) then a plan group (5h meter ·
// week meter · extra). Real data from usageForSession; every row hides when its
// datum is missing (house rule — nothing fabricated). NOT the old UsagePanel.
function UsageSheetBody({ session, usage, busy }) {
  const u = usageForSession(session, usage);
  const ctx = session.contextPercent;
  const hasCtx = typeof ctx === "number" && ctx >= 0;
  const hasCost = typeof session.costUSD === "number" && session.costUSD > 0;
  const extra = u.extra;
  const extraMoney = (v) =>
    money(v, { decimal_places: extra.decimalPlaces, currency: extra.currency });
  const planReset = (m) =>
    m.source === "anthropic" && m.resetsAt ? `resets in ${fmtReset(m.resetsAt)}` : "";

  return (
    <div class="msl-usage">
      {(hasCost || hasCtx) && (
        <div class="msl-ugroup">
          {hasCost && (
            <div class="msl-urow">
              <span class="uk">Session</span>
              <span class="uv">~{fmtCost(session.costUSD)}</span>
            </div>
          )}
          {hasCtx && (
            <div class="msl-urow">
              <span class="uk">Context</span>
              <span class="uv">{ctx}%</span>
            </div>
          )}
        </div>
      )}

      {(u.fiveHour || u.week || extra) && (
        <div class="msl-ugroup">
          {u.fiveHour && (
            <div class="msl-urow">
              <span class="uk">Plan · 5h</span>
              <span class={`umeter u-${usageLevel(u.fiveHour.pct)}`} aria-hidden="true">
                <span style={{ width: `${Math.min(100, Math.max(0, u.fiveHour.pct))}%` }} />
              </span>
              <span class="uv">{u.fiveHour.pct}%</span>
              {planReset(u.fiveHour) && <span class="unote">{planReset(u.fiveHour)}</span>}
            </div>
          )}
          {u.week && (
            <div class="msl-urow">
              <span class="uk">Plan · week</span>
              <span class={`umeter u-${usageLevel(u.week.pct)}`} aria-hidden="true">
                <span style={{ width: `${Math.min(100, Math.max(0, u.week.pct))}%` }} />
              </span>
              <span class="uv">{u.week.pct}%</span>
              {planReset(u.week) && <span class="unote">{planReset(u.week)}</span>}
            </div>
          )}
          {extra && (
            <div class="msl-urow">
              <span class="uk">Extra</span>
              <span class="uv">
                {extraMoney(extra.used)}
                {extra.limit != null && ` of ${extraMoney(extra.limit)}`}
              </span>
              <span class="unote">pay-as-you-go</span>
            </div>
          )}
        </div>
      )}

      <ContextLimitRow session={session} disabled={busy} />
    </div>
  );
}

export function MobileStatusLine({ session, usage }) {
  const [usageOpen, setUsageOpen] = useState(false);
  const [sessionOpen, setSessionOpen] = useState(false);
  const [permsOpen, setPermsOpen] = useState(false);
  const [models, setModels] = useState(null); // null = not fetched yet

  const sessionId = session ? session.id : null;
  useEffect(() => {
    setUsageOpen(false);
    setSessionOpen(false);
    setPermsOpen(false);
  }, [sessionId]);

  useEffect(() => {
    if (!sessionOpen || models) return;
    api("GET", "/api/models").then(setModels).catch(() => setModels([]));
  }, [sessionOpen, models]);

  const hasSession = !!session;
  const ctx = hasSession ? session.contextPercent : undefined;
  const hasCtx = typeof ctx === "number" && ctx >= 0;
  const model = statusStripModel(session, usage);
  const spend = hasSession && session.costUSD > 0 ? fmtCost(session.costUSD) : undefined;
  const busy = hasSession && (session.state === "running" || session.state === "permission");
  // Per-run token heartbeat — shown only once the run has actually moved any,
  // so an idle session's line stays quiet rather than reading a hollow "↑0 ·↓0".
  const tokensUp = hasSession ? session.runTokensUp : undefined;
  const tokensDown = hasSession ? session.runTokensDown : undefined;
  const hasTokens = (tokensUp || 0) > 0 || (tokensDown || 0) > 0;

  const specs = deriveModelSpecs(models);
  const thinking = hasSession
    ? session.thinking === "none"
      ? "off"
      : session.thinking || "off"
    : "off";
  const modelName = hasSession
    ? modelCodename(session.model) || shortModel(session.model) || session.model || ""
    : "";
  const permMode = model.perm.mode;

  // Ring geometry: teal arc over a surface1 track, matching the mock's SVG ring
  // (pathLength 100 → dasharray "pct 100-pct"). Kept as SVG (not a conic mask) so
  // the stroke width reads exactly like the approved capture.
  const ringPct = hasCtx ? Math.min(100, Math.max(0, ctx)) : 0;

  const changePerm = (value) => {
    if (value !== permMode) configureSession(session.id, { permissionMode: value });
    setPermsOpen(false);
  };

  return (
    <div class="mstatusline">
      {hasCtx && (
        <button
          type="button"
          class="msl-ctx"
          onClick={() => setUsageOpen(true)}
          aria-haspopup="dialog"
          aria-expanded={usageOpen}
          aria-label={
            spend
              ? `Context ${ctx}% used, ~${spend} spent — open context and usage`
              : `Context ${ctx}% used — open context and usage`
          }
        >
          <svg class="msl-ring" viewBox="0 0 36 36" aria-hidden="true">
            <circle class="t" cx="18" cy="18" r="15.5" pathLength="100" />
            <circle
              class="f"
              cx="18"
              cy="18"
              r="15.5"
              pathLength="100"
              stroke-dasharray={`${ringPct} ${100 - ringPct}`}
            />
          </svg>
          <span class="msl-ctx-pct">{ctx}%</span>
          {spend && (
            <span class={`msl-cost spend-${model.spendLevel || "normal"}`} aria-hidden="true">
              ~{spend}
            </span>
          )}
        </button>
      )}

      {hasCtx && hasSession && <span class="msl-div" aria-hidden="true" />}

      {hasSession && (
        <ModelPill
          model={modelName}
          accent={modelAccent(session.model)}
          variant="bars"
          level={thinking}
          onClick={() => setSessionOpen(true)}
          aria-haspopup="dialog"
          aria-expanded={sessionOpen}
          aria-label="Model and thinking — change"
        />
      )}

      {hasSession && <span class="msl-div" aria-hidden="true" />}

      {hasSession && (
        <button
          type="button"
          class="msl-perm"
          onClick={() => setPermsOpen(true)}
          aria-haspopup="dialog"
          aria-expanded={permsOpen}
          aria-label={`Permission mode: ${permMode.toUpperCase()} — change`}
        >
          <span class={`perm-chip perm-${permMode}`} aria-hidden="true">
            {permMode}
          </span>
        </button>
      )}

      <span class="msl-spacer" aria-hidden="true" />

      {hasTokens && (
        <span class="msl-tokens">
          <TokenFlow up={tokensUp} down={tokensDown} variant="compact" />
        </span>
      )}

      {hasSession && (
        <MobileSheet
          open={usageOpen}
          onClose={() => setUsageOpen(false)}
          title="Context & usage"
          scope="this session"
        >
          <UsageSheetBody session={session} usage={usage} busy={busy} />
        </MobileSheet>
      )}

      {hasSession && (
        <MobileSheet
          open={sessionOpen}
          onClose={() => setSessionOpen(false)}
          // No `scope` here: the path was this sheet's header back when it was
          // "This session" and the question was where the session runs. On a
          // sheet that only picks a model it is one more piece of session info
          // nobody asked for — and long enough to wrap the title onto two
          // lines. The cwd lives with the sessions, in the drawer.
          title="Model & thinking"
        >
          <ModelSelector
            models={specs}
            selected={matchSelectedModel(specs, session.model)}
            thinking={thinking}
            embedded
            sessionModel={session.model || ""}
            onSelect={(spec) => configureSession(session.id, { model: spec })}
            onThinkingChange={(value) => configureSession(session.id, { thinking: value })}
          />

        </MobileSheet>
      )}

      {hasSession && (
        <MobileSheet
          open={permsOpen}
          onClose={() => setPermsOpen(false)}
          title="Permissions"
          scope="this session"
        >
          <div class="perm-sheet-list" role="menu" aria-label="Permission mode">
            {PERM_MODES.map((m) => {
              const on = m.value === permMode;
              return (
                <button
                  key={m.value}
                  type="button"
                  role="menuitemradio"
                  aria-checked={on}
                  class={`perm-menu-item${on ? " on" : ""}`}
                  disabled={busy && !on}
                  onClick={() => changePerm(m.value)}
                >
                  <span class="perm-menu-check" aria-hidden="true">{on && <Check />}</span>
                  <span class="perm-menu-text">
                    <span class={`perm-menu-label perm-${m.value}`}>{m.label}</span>
                    <span class="perm-menu-desc">{m.desc}</span>
                  </span>
                </button>
              );
            })}
          </div>
        </MobileSheet>
      )}
    </div>
  );
}
