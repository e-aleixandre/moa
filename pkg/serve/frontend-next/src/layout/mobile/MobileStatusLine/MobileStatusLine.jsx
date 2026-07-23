import { useState, useEffect, useRef } from "preact/hooks";
import { PanelsTopLeft, ChevronUp, Bell, RotateCcw, Check, SlidersHorizontal, Settings2, Shield } from "lucide-preact";
import { ModelSelector, ModelPill } from "../../../components/index.js";
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
import { shortPath, modelCodename, shortModel } from "../../../data/util/format.js";
import { MobileSheet } from "./MobileSheet/MobileSheet.jsx";
import "./MobileStatusLine.css";

// MobileStatusLine — the persistent mobile chrome (FOUR DOORS), literal parity
// with the approved mock (future-control-ia/a.html + fx.css). One line pinned
// under the composer holding four single-scope controls, each its own hit area
// and destination, each opening the approved bottom sheet (MobileSheet) that
// STOPS above the composer — never the centered generic <Sheet> modal.
//
//   1. context ring + cost (LEFT) — the canonical teal SVG ring + pct + the
//      estimated session cost (~, spend severity). Tap → "Context & usage": the
//      approved usage sheet (session cost · context · plan windows · extra).
//   2. ModelPill — current model + thinking. Tap → "This session": the real
//      ModelSelector, then a planned session-settings section, then Rewind; the
//      working directory is a fact in the sheet header (scope), not a row.
//   3. permission chip — the glanceable safety color AND the door. ONE tap
//      reveals the complete YOLO/AUTO/ASK choice directly in the sheet (no
//      intermediate chip, no second tap).
//   4. the Sessions control (RIGHT) — always visible, explicitly labelled, opens
//      the SessionDrawer list immediately, and aggregates cross-session
//      attention as the desktop attn-lamp (Bell + count, breathing).
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

// UsageSheetBody — the approved "Context & usage" presentation (fx-shared
// FX_USAGE_BODY): a session group (cost · context) then a plan group (5h meter ·
// week meter · extra). Real data from usageForSession; every row hides when its
// datum is missing (house rule — nothing fabricated). NOT the old UsagePanel.
function UsageSheetBody({ session, usage }) {
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
    </div>
  );
}

export function MobileStatusLine({
  session,
  usage,
  attnCount = 0,
  onOpenSessions,
  onRewind,
  rewindDisabled = false,
}) {
  const [usageOpen, setUsageOpen] = useState(false);
  const [sessionOpen, setSessionOpen] = useState(false);
  const [permsOpen, setPermsOpen] = useState(false);
  const [models, setModels] = useState(null); // null = not fetched yet
  // Rewind is handed off only AFTER the "This session" sheet has fully
  // dismissed (MobileSheet.onClosed) — opening the RewindTimeline while the
  // outgoing sheet is still closing would stack it above the outgoing sheet and
  // race the shared overlay-history back()/popstate, so the timeline appeared to
  // close immediately. This flag remembers a Rewind tap across that transition;
  // a plain close (swipe/escape/backdrop) leaves it false and hands nothing off.
  const rewindPendingRef = useRef(false);

  const sessionId = session ? session.id : null;
  useEffect(() => {
    setUsageOpen(false);
    setSessionOpen(false);
    setPermsOpen(false);
    rewindPendingRef.current = false;
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
  const hasAttn = attnCount > 0;

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
  const path = hasSession ? shortPath(session.cwd) || session.cwd || "" : "";

  // Ring geometry: teal arc over a surface1 track, matching the mock's SVG ring
  // (pathLength 100 → dasharray "pct 100-pct"). Kept as SVG (not a conic mask) so
  // the stroke width reads exactly like the approved capture.
  const ringPct = hasCtx ? Math.min(100, Math.max(0, ctx)) : 0;

  const changePerm = (value) => {
    if (value !== permMode) configureSession(session.id, { permissionMode: value });
    setPermsOpen(false);
  };

  return (
    <div class={`mstatusline${hasAttn ? " has-attn" : ""}`}>
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

      <span class="msl-spacer" aria-hidden="true" />

      {hasSession && (
        <ModelPill
          model={modelName}
          accent={modelAccent(session.model)}
          variant="glyph"
          level={thinking}
          onClick={() => setSessionOpen(true)}
          aria-haspopup="dialog"
          aria-expanded={sessionOpen}
          aria-label="This session — model, thinking and details"
        />
      )}

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
            <Shield />
            {permMode.toUpperCase()}
          </span>
        </button>
      )}

      <button
        type="button"
        class="msl-sessions"
        onClick={onOpenSessions}
        aria-haspopup="dialog"
        aria-label={hasAttn ? `Sessions — ${attnCount} other session${attnCount === 1 ? "" : "s"} need you` : "Sessions"}
      >
        <span class="msl-sessions-icon" aria-hidden="true">
          <PanelsTopLeft size={14} />
        </span>
        <span class="msl-sessions-label">Sessions</span>
        {hasAttn && (
          <span class="msl-attn" aria-hidden="true">
            <Bell size={12} aria-hidden="true" />
            <span class="msl-attn-n">{attnCount}</span>
          </span>
        )}
        <ChevronUp size={12} class="msl-sessions-chev" aria-hidden="true" />
      </button>

      {hasSession && (
        <MobileSheet
          open={usageOpen}
          onClose={() => setUsageOpen(false)}
          title="Context & usage"
          scope="this session"
        >
          <UsageSheetBody session={session} usage={usage} />
        </MobileSheet>
      )}

      {hasSession && (
        <MobileSheet
          open={sessionOpen}
          onClose={() => setSessionOpen(false)}
          onClosed={() => {
            if (!rewindPendingRef.current) return;
            rewindPendingRef.current = false;
            onRewind && onRewind();
          }}
          title="This session"
          scope={path}
        >
          <div class="msl-lbl">Model &amp; thinking</div>
          <ModelSelector
            models={specs}
            selected={matchSelectedModel(specs, session.model)}
            thinking={thinking}
            embedded
            onSelect={(spec) => configureSession(session.id, { model: spec })}
            onThinkingChange={(value) => configureSession(session.id, { thinking: value })}
          />

          <div class="msl-lbl">
            Session settings <span class="msl-future">planned</span>
          </div>
          <div class="msl-row is-planned">
            <span class="lead"><SlidersHorizontal size={15} aria-hidden="true" /></span>
            <span class="k">Context limit</span>
            <span class="v">auto</span>
          </div>
          <div class="msl-row is-planned">
            <span class="lead"><Settings2 size={15} aria-hidden="true" /></span>
            <span class="k">MCP servers</span>
            <span class="v">—</span>
          </div>

          <div class="msl-acts">
            <button
              type="button"
              class="msl-act"
              disabled={rewindDisabled || !onRewind}
              onClick={() => {
                rewindPendingRef.current = true;
                setSessionOpen(false);
              }}
            >
              <RotateCcw size={13} aria-hidden="true" /> Rewind
            </button>
          </div>
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
