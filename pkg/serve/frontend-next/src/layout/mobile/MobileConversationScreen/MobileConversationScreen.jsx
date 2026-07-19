import { useState, useEffect } from "preact/hooks";
import { store } from "../../../data/store.js";
import { projectStream } from "../../../data/stream-model.js";
import { focusedSession, focusedSessionId } from "../../../data/selectors.js";
import { setActiveSession } from "../../../data/tile-actions.js";
import { openPalette } from "../../../data/palette.js";
import { shortModel, shortPath, sessionDotState } from "../../../data/util/format.js";
import { activityPhase } from "../../../data/util/activity.js";
import { Composer } from "../../Composer/Composer.jsx";
import { PermissionPrompt, AskUserPrompt, McpBanner } from "../../../components/index.js";
import { MobileHeader } from "../MobileHeader/MobileHeader.jsx";
import { SessionStrip } from "../SessionStrip/SessionStrip.jsx";
import { MobileComposer } from "../MobileComposer/MobileComposer.jsx";
import { SessionDrawer } from "../SessionDrawer/SessionDrawer.jsx";
import { MobileStream } from "./MobileStream.jsx";
import "./MobileConversationScreen.css";

// MobileConversationScreen — the CONNECTED root container of the mobile
// conversation screen (5I). It replaces the 4A mock (hardcoded SESSIONS /
// READ_ROWS / conversation) with the SAME store-driven wiring as the desktop
// ConversationScreen (5C): subscribe to the store, derive the focused (active)
// session, project its stream, and pass real props down to the presentational
// mobile chrome (header / strip / drawer) + the SHARED content components (via
// MobileStream) + the REAL Composer.
//
// Architecture (OPTION B): the mobile screen reuses the desktop's data
// projection (projectStream) and shared components; the only divergence is the
// mobile layout chrome and the ledger→MobileLedger remap (MobileStream). No
// data logic is duplicated. The mock specimen used by the design gallery now
// lives in mobile-gallery.jsx (see MobileConversationSpecimen there).

// relAge — coarse relative age from an `updated` epoch (mirrors the desktop
// ConversationScreen's spineSessions helper; kept local to avoid a shared
// import churn). "now" under a minute, then m/h/d.
function relAge(updated) {
  if (!updated) return "";
  const diff = Date.now() - updated;
  const min = Math.floor(diff / 60000);
  if (min < 1) return "now";
  if (min < 60) return `${min}m`;
  const h = Math.floor(min / 60);
  if (h < 24) return `${h}h`;
  const d = Math.floor(h / 24);
  return `${d}d`;
}

// sessionBrief derives the drawer card's "last" line from the real session
// fields the /api/sessions poll carries: the server-owned brief (progress →
// attempting) when present, else the live activity label, else the raw state.
// There is NO per-session "last message" field in the poll model, so we DO NOT
// invent a summary — we degrade to the brief, then to the activity/state.
// // TODO(5x): a true last-message preview would need the projection or the API
// to carry one; not added here (out of scope, would touch the backend/model).
function sessionBrief(sess) {
  if (sess.briefProgress) return sess.briefProgress;
  if (sess.briefAttempting) return sess.briefAttempting;
  if (sess.error) return sess.error;
  const phase = activityPhase(sess);
  if (phase === "waiting") return "Waiting for you";
  if (sess.state === "running") return "Working…";
  if (sess.state === "saved") return "Saved";
  return sess.state || "idle";
}

// stripSessions builds the horizontal SessionStrip's chip list: active (non-
// saved, non-archived) sessions, newest first. `needs` = a blocking prompt.
function stripSessions(sessions) {
  return Object.values(sessions)
    .filter((s) => !s.archived && s.state !== "saved")
    .sort((a, b) => (b.updated || 0) - (a.updated || 0))
    .map((s) => ({
      id: s.id,
      name: s.title || s.id,
      state: sessionDotState(s),
      unseen: !!s.unseen,
      needs: !!(s.pendingPerm || s.pendingAsk || s.state === "permission"),
    }));
}

// drawerSessions builds the full bottom-sheet list: active first (newest), then
// saved. activeCount/savedCount come from the two groups.
function drawerSessions(sessions, activeId) {
  const all = Object.values(sessions).filter((s) => !s.archived);
  const active = all
    .filter((s) => s.state !== "saved")
    .sort((a, b) => (b.updated || 0) - (a.updated || 0));
  const saved = all
    .filter((s) => s.state === "saved")
    .sort((a, b) => (b.updated || 0) - (a.updated || 0));
  const toCard = (s) => {
    const needs = !!(s.pendingPerm || s.pendingAsk || s.state === "permission");
    return {
      id: s.id,
      title: s.title || s.id,
      state: sessionDotState(s),
      when: relAge(s.updated),
      last: sessionBrief(s),
      needsLabel: needs ? "Needs you:" : undefined,
      path: shortPath(s.cwd) || s.cwd || "",
      unseen: !!s.unseen,
      active: s.id === activeId,
    };
  };
  return {
    list: [...active, ...saved].map(toCard),
    activeCount: active.length,
    savedCount: saved.length,
  };
}

export function MobileConversationScreen() {
  const [state, setState] = useState(store.get());
  useEffect(() => store.subscribe(setState), []);

  const [drawerOpen, setDrawerOpen] = useState(false);

  const session = focusedSession(state);
  const activeId = focusedSessionId(state);
  const loaded = state.sessionsLoaded;

  const onSelect = (id) => setActiveSession(id);
  const onSelectFromDrawer = (id) => { setActiveSession(id); setDrawerOpen(false); };
  const onNew = () => openPalette("create");

  const strip = stripSessions(state.sessions);
  const { list: drawerList, activeCount, savedCount } = drawerSessions(state.sessions, activeId);

  let body;
  if (!loaded) {
    body = <div class="mconv-placeholder">Loading sessions…</div>;
  } else if (!session) {
    body = (
      <div class="mconv-empty">
        <p class="mconv-empty-title">No active session</p>
        <p class="mconv-empty-hint">Open the sessions sheet to pick one, or start a new one.</p>
      </div>
    );
  } else {
    const blocks = projectStream(session);
    const blocking =
      session.untrustedMcp || session.pendingPerm || session.pendingAsk;
    body = (
      <>
        <MobileStream session={session} blocks={blocks} />
        {blocking && (
          <div class="mconv-blocking">
            {session.untrustedMcp && <McpBanner key={session.id} sessionId={session.id} />}
            {session.pendingPerm && <PermissionPrompt key={session.id} session={session} />}
            {session.pendingAsk && <AskUserPrompt key={session.id} session={session} />}
          </div>
        )}
        <MobileComposer key={session.id} session={session} />
      </>
    );
  }

  return (
    <div class="mconv">
      <MobileHeader
        state={session ? session.state || "idle" : "idle"}
        title={session ? session.title || session.id : "moa"}
        model={session ? shortModel(session.model) || session.model || "" : ""}
        level={session ? session.thinking || "off" : "off"}
        path={session ? shortPath(session.cwd) || session.cwd || "" : ""}
        ctx={session ? session.contextPercent : undefined}
        onOpenSessions={() => setDrawerOpen(true)}
      />
      <SessionStrip
        sessions={strip}
        activeId={activeId}
        onSelect={onSelect}
        onNew={onNew}
      />

      {body}

      <SessionDrawer
        open={drawerOpen}
        onClose={() => setDrawerOpen(false)}
        sessions={drawerList}
        activeCount={activeCount}
        savedCount={savedCount}
        onSelect={onSelectFromDrawer}
        onNew={onNew}
      />
    </div>
  );
}
