import { useEffect, useRef } from "preact/hooks";
import { ArrowLeft, Clock3, FileText, Plug, X } from "lucide-preact";
import { useStore } from "../../hooks/useStore.js";
import { registerOverlay } from "../../data/overlays.js";
import { openOverlay } from "../../data/overlay-history.js";
import {
  PANEL_PAGES, artifactsVerdict, closeSessionPanel, mcpVerdict, runFacts,
  setSessionPanelPage, usageVerdict,
} from "../../data/session-panel.js";
import { artifactsSlice, loadArtifacts, openArtifactsList } from "../../data/artifacts.js";
import { UsagePanel } from "../UsagePanel/UsagePanel.jsx";
import { McpPanel } from "../McpPanel/McpPanel.jsx";
import { ArtifactRow } from "../Artifacts/ArtifactRow.jsx";
import { closeSession, deleteSession, resumeSession } from "../../data/session-actions.js";
import { sessionTitle, shortPath } from "../../data/util/format.js";
import { renameSession } from "../../data/session-actions.js";
import { addToast } from "../../data/notifications.js";
import "./SessionPanel.css";

// SessionPanel — the session's DOSSIER, in a right-hand drawer.
//
// The rule (PANEL-CRITERIO-FABLE): the status line holds the controls for the
// NEXT turn — model, thinking, permissions, fast — and this panel holds what
// the session IS and what it HAS DONE. Model and permissions are deliberately
// absent, not even as a reading: a datum with two homes is a datum that drifts,
// and a control you can see but not touch is a false affordance.
//
// Its second level is a PUSH, not a modal: Usage, MCP and Artifacts are rows,
// and the one you open replaces the body while the head swaps its eyebrow for
// back + title. Nothing ever floats over the panel — a modal on top of a drawer
// is the definition of a surface without a home.

function PanelRow({ id, icon: Icon, title, verdict, onOpen }) {
  return (
    <button
      type="button"
      class="spanel-row"
      onClick={() => onOpen(id)}
      aria-label={`${title}: ${verdict.text}`}
    >
      <Icon size={15} aria-hidden="true" class="spanel-row-ico" />
      <span class="spanel-row-t">{title}</span>
      <span class={`spanel-row-v${verdict.warn ? " is-warn" : ""}`}>{verdict.text}</span>
      <span class="spanel-row-go" aria-hidden="true">›</span>
    </button>
  );
}

// ArtifactsPage — the LIST, never the reader. Choosing one hands off to the
// existing shared drawer, which is where a document is actually readable: the
// reader never lives in 340px. That is also why the two right-hand surfaces
// cannot collide — opening an artifact closes this panel on its way out.
function ArtifactsPage({ sessionId }) {
  const slice = useStore(artifactsSlice);
  const mine = slice.ownerSessionId === sessionId;
  const items = mine ? slice.items : [];

  // The collection is server state: load it on open like every other consumer
  // rather than trusting whatever the drawer last held.
  useEffect(() => {
    if (!sessionId) return;
    if (mine && slice.status === "ready") return;
    loadArtifacts(sessionId);
  }, [sessionId]);

  const open = (artifact) => {
    closeSessionPanel();
    openArtifactsList(sessionId);
    // The list is already loading for this conversation; the drawer opens on
    // its own list view, where the reader has the width it needs.
    void artifact;
  };

  if (mine && slice.status === "loading" && items.length === 0) {
    return <p class="spanel-page-sum">Loading…</p>;
  }
  if (items.length === 0) {
    return <p class="spanel-page-sum">No files in this conversation yet. Ask the agent to send you one.</p>;
  }
  return (
    <>
      <p class="spanel-page-sum">
        {items.length === 1 ? "1 file" : `${items.length} files`} in this conversation. Opening one shows it in the reader.
      </p>
      <ul class="spanel-artifacts">
        {items.map((entry) => (
          <li key={entry.id}>
            <ArtifactRow artifact={entry} onOpen={open} />
          </li>
        ))}
      </ul>
    </>
  );
}

function LifecycleActions({ session }) {
  const saved = session.state === "saved";
  const confirm = useRef(null);
  return (
    <div class="spanel-acts">
      {saved ? (
        <button
          type="button"
          class="spanel-act"
          onClick={() => { resumeSession(session.id).catch(() => {}); }}
        >
          <span class="spanel-act-t">Reopen session</span>
          <span class="spanel-act-d">Loads it back into memory and picks up where it left off.</span>
        </button>
      ) : (
        <button
          type="button"
          class="spanel-act"
          onClick={() => { closeSession(session.id).then(closeSessionPanel).catch(() => {}); }}
        >
          <span class="spanel-act-t">Save for later</span>
          <span class="spanel-act-d">Stops the agent and keeps the session in Saved.</span>
        </button>
      )}
      <button
        type="button"
        class="spanel-act is-danger"
        ref={confirm}
        onClick={(event) => {
          const node = event.currentTarget;
          if (node.dataset.confirming !== "true") {
            node.dataset.confirming = "true";
            return;
          }
          deleteSession(session.id).then(closeSessionPanel).catch(() => {});
        }}
      >
        <span class="spanel-act-t">Delete session…</span>
        <span class="spanel-act-d">Removes it for good. Click again to confirm.</span>
      </button>
    </div>
  );
}

export function SessionPanel({ session, usage, open, page = "root", variant = "" }) {
  const panelRef = useRef(null);
  const sub = page !== "root";

  // The back gesture / browser Back closes the panel, and while a page is
  // pushed it returns to the root first — one entry per level, the same
  // contract every sheet in the app keeps.
  useEffect(() => {
    if (!open) return undefined;
    const close = openOverlay("session-panel", () => closeSessionPanel());
    return () => close();
  }, [open]);
  useEffect(() => {
    if (!open || !sub) return undefined;
    const close = openOverlay("session-panel-page", () => setSessionPanelPage("root"));
    return () => close();
  }, [open, sub]);

  useEffect(() => {
    if (!open) return undefined;
    const unregister = registerOverlay("session-panel");
    const onKey = (event) => {
      if (event.key !== "Escape") return;
      event.stopPropagation();
      if (sub) setSessionPanelPage("root");
      else closeSessionPanel();
    };
    document.addEventListener("keydown", onKey);
    return () => {
      unregister();
      document.removeEventListener("keydown", onKey);
    };
  }, [open, sub]);

  if (!session) return null;

  const facts = runFacts(session);
  const mcp = mcpVerdict(session);

  return (
    <aside
      ref={panelRef}
      class={`spanel${open ? " is-open" : ""}${variant ? ` spanel-${variant}` : ""}`}
      role="dialog"
      aria-label="This session"
      aria-hidden={!open}
    >
      <div class={`spanel-head${sub ? " is-sub" : ""}`}>
        {sub ? (
          <>
            <button
              type="button"
              class="spanel-back"
              onClick={() => setSessionPanelPage("root")}
              aria-label="Back to this session"
            >
              <ArrowLeft size={16} aria-hidden="true" />
            </button>
            <span class="spanel-title is-page">{PANEL_PAGES[page]}</span>
          </>
        ) : (
          <span class="spanel-title is-eyebrow">This session</span>
        )}
        <button type="button" class="spanel-x" onClick={closeSessionPanel} aria-label="Close">
          <X size={16} aria-hidden="true" />
        </button>
      </div>

      {sub ? (
        <div class="spanel-body is-sub" key={page}>
          {page === "usage" && (
            <UsagePanel
              session={session}
              usage={usage}
              ctxPercent={session.contextPercent}
              costUSD={session.costUSD}
            />
          )}
          {page === "mcp" && <McpPanel sessionId={session.id} mcpTick={session.mcpTick} />}
          {page === "artifacts" && <ArtifactsPage sessionId={session.id} />}
        </div>
      ) : (
        <>
          <div class="spanel-body">
            <SessionIdentity session={session} />
            {facts.length > 0 && (
              <dl class="spanel-facts">
                {facts.map((fact) => (
                  <div key={fact.id}>
                    <dt>{fact.label}</dt>
                    <dd>{fact.value}</dd>
                  </div>
                ))}
              </dl>
            )}
            <div class="spanel-rows">
              <PanelRow
                id="usage"
                icon={Clock3}
                title="Usage"
                verdict={usageVerdict(session, usage)}
                onOpen={setSessionPanelPage}
              />
              {mcp && (
                <PanelRow id="mcp" icon={Plug} title="MCP" verdict={mcp} onOpen={setSessionPanelPage} />
              )}
              <ArtifactsRow sessionId={session.id} />
            </div>
          </div>
          <LifecycleActions session={session} />
        </>
      )}
    </aside>
  );
}

function ArtifactsRow({ sessionId }) {
  const slice = useStore(artifactsSlice);
  const mine = slice.ownerSessionId === sessionId;
  const count = mine ? slice.items.length : 0;
  return (
    <PanelRow
      id="artifacts"
      icon={FileText}
      title="Artifacts"
      verdict={artifactsVerdict(count, mine ? slice.status : "idle")}
      onOpen={setSessionPanelPage}
    />
  );
}

// SessionIdentity — the name is editable HERE, and nowhere else in the UI: the
// only other way to rename was typing /rename into the composer. The folder and
// the meta line are read-only facts of where the session runs.
function SessionIdentity({ session }) {
  const inputRef = useRef(null);
  const title = sessionTitle(session);

  const commit = (event) => {
    const next = event.currentTarget.value.trim();
    if (!next || next === title) {
      event.currentTarget.value = title;
      return;
    }
    renameSession(session.id, next).catch((error) => {
      event.currentTarget.value = title;
      addToast({
        title: "Could not rename the session",
        detail: String(error.message || error),
        type: "error",
      });
    });
  };

  // The start time, and the day when it was not today: a bare "09:12" on a
  // three-day-old session says the wrong thing. The branch the prototype also
  // showed here is deliberately absent: no API field carries it, and the last
  // path segment only looks like a branch in a worktree layout — printing it
  // would be a guess dressed as a fact.
  const startedAt = session.created ? new Date(session.created) : null;
  const started = startedAt
    ? (() => {
      const time = startedAt.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
      const today = new Date().toDateString() === startedAt.toDateString();
      return today ? time : `${startedAt.toLocaleDateString([], { day: "numeric", month: "short" })} ${time}`;
    })()
    : "";

  return (
    <>
      <label class="spanel-field">
        <span class="spanel-label">Name</span>
        <input
          class="spanel-input"
          ref={inputRef}
          defaultValue={title}
          key={`${session.id}:${title}`}
          onBlur={commit}
          onKeyDown={(event) => {
            if (event.key === "Enter") event.currentTarget.blur();
            if (event.key === "Escape") {
              event.currentTarget.value = title;
              event.currentTarget.blur();
            }
          }}
          aria-label="Session name"
        />
      </label>
      <div class="spanel-field">
        <span class="spanel-label">Folder</span>
        <div class="spanel-input is-static">{shortPath(session.cwd, 64) || session.cwd || "—"}</div>
        {started && <div class="spanel-field-meta">started {started}</div>}
      </div>
    </>
  );
}
