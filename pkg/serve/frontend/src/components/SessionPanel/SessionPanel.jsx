import { useEffect, useRef } from "preact/hooks";
import { useStore } from "../../hooks/useStore.js";
import { registerOverlay } from "../../data/overlays.js";
import {
  PANEL_PAGES, artifactsVerdict, closeSessionPanel, mcpVerdict, runFacts,
  setSessionPanelPage, usageVerdict,
} from "../../data/session-panel.js";
import { artifactsSlice, listArtifactsInPanel, openArtifactsList } from "../../data/artifacts.js";
import { UsagePage } from "./UsagePage.jsx";
import { McpPage } from "./McpPage.jsx";
import { closeSession, deleteSession, resumeSession } from "../../data/session-actions.js";
import { sessionTitle, shortPath } from "../../data/util/format.js";
import { renameSession } from "../../data/session-actions.js";
import { addToast } from "../../data/notifications.js";
import { OwnerPanelAvatar, OwnerPanelPage } from "../Owners/Owners.jsx";
import "./SessionPanel.css";

// SessionPanel — the session's DOSSIER, in a right-hand drawer.
//
// Markup and CSS are the catalogue's (catalog/zones-lab.jsx `SessionPanel`,
// zones-lab.css the `.zl-side-right` / `.zl-panel*` block), MOVED here rather
// than imitated: the classes travelled with the rules, so the panel IS the
// accepted design instead of a translation of it. The catalogue imports this
// component now, which is what makes one definition rather than two.
//
// What is NOT the catalogue's is everything the prototype never had, grafted
// on top: real sessions, the overlay stack, rename, lifecycle, the artifacts
// claim that does not open the reader (`listArtifactsInPanel` / view:'panel'),
// and the house rule that a missing datum hides its row rather than drawing a
// zero.
//
// The rule (PANEL-CRITERIO-FABLE): the status line holds the controls for the
// NEXT turn — model, thinking, permissions, fast — and this panel holds what
// the session IS and what it HAS DONE. Model and permissions are deliberately
// absent. Its second level is a PUSH, not a modal.

const PANEL_ICONS = {
  overview: <path d="M3 3.5h10v9H3z M5.5 6.5h5M5.5 9h3" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" />,
  book: <path d="M3 3.5h4.5c1 0 1.5.5 1.5 1.5v7c0-1-.5-1.5-1.5-1.5H3zM13 3.5H8.5C7.5 3.5 7 4 7 5v7c0-1 .5-1.5 1.5-1.5H13z" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linejoin="round" />,
  usage: <><circle cx="8" cy="8" r="6" fill="none" stroke="currentColor" stroke-width="1.5" /><path d="M8 8V4.5M8 8l2.5 2" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" /></>,
  mcp: <path d="M5 2v3M11 2v3M3.5 5h9v3a4.5 4.5 0 0 1-9 0zM8 12.5V15" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />,
  artifacts: <path d="M3.5 2.5h6l3 3v8h-9z M9.5 2.5v3h3" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round" />,
};

function BackIcon() {
  return (
    <svg viewBox="0 0 16 16" aria-hidden="true">
      <path d="M10 3.5L5.5 8l4.5 4.5" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" />
    </svg>
  );
}
function GoIcon() {
  return (
    <svg class="zl-go" viewBox="0 0 12 12" aria-hidden="true">
      <path d="M4 2.5L7.5 6 4 9.5" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" />
    </svg>
  );
}

function PanelRow({ id, title, verdict = "", warn, tone, onOpen }) {
  const verdictTone = warn ? (tone === "warn" ? " is-warn-soft" : " is-warn") : "";
  return (
    <button type="button" class="zl-prow" onClick={() => onOpen(id)} aria-label={verdict ? `${title}: ${verdict}` : title}>
      <svg class="zl-prow-ico" viewBox="0 0 16 16" aria-hidden="true">{PANEL_ICONS[id]}</svg>
      <span class="zl-prow-t">{title}</span>
      {verdict && <span class={`zl-prow-v zl-data${verdictTone}`}>{verdict}</span>}
      <GoIcon />
    </button>
  );
}

function splitPath(cwd) {
  const p = shortPath(cwd, 64) || cwd || "";
  const i = p.lastIndexOf("/");
  if (i <= 0) return { dir: "", base: p || "—" };
  return { dir: p.slice(0, i + 1), base: p.slice(i + 1) };
}

function fmtSize(n) {
  const v = Number(n) || 0;
  if (v < 1024) return `${v} B`;
  const k = v / 1024;
  if (k < 10) return `${k.toFixed(1)} kB`;
  if (k < 1024) return `${Math.round(k)} kB`;
  return `${(v / (1024 * 1024)).toFixed(1)} MB`;
}

function LifecycleActions({ session, inline }) {
  const saved = session.state === "saved";
  const confirm = useRef(null);
  const run = (fn) => {
    if (inline) return;
    fn();
  };
  return (
    <div class="zl-panel-acts">
      {saved ? (
        <button
          type="button"
          class="zl-act"
          onClick={() => run(() => { resumeSession(session.id).catch(() => {}); })}
        >
          <span class="zl-act-t">Reopen session</span>
          <span class="zl-act-d">Loads it back into memory and picks up where it left off.</span>
        </button>
      ) : (
        <button
          type="button"
          class="zl-act"
          onClick={() => run(() => { closeSession(session.id).then(closeSessionPanel).catch(() => {}); })}
        >
          <span class="zl-act-t">Save for later</span>
          <span class="zl-act-d">Stops the agent, keeps the session in Saved.</span>
        </button>
      )}
      <button
        type="button"
        class="zl-act is-danger"
        ref={confirm}
        onClick={(event) => {
          if (inline) return;
          const node = event.currentTarget;
          if (node.dataset.confirming !== "true") {
            node.dataset.confirming = "true";
            return;
          }
          deleteSession(session.id).then(closeSessionPanel).catch(() => {});
        }}
      >
        <span class="zl-act-t">{inline ? "Close session" : "Delete session…"}</span>
        <span class="zl-act-d">
          {inline
            ? "Removes it from the list. The transcript stays on disk."
            : "Removes it for good. Click again to confirm."}
        </span>
      </button>
    </div>
  );
}

export function SessionPanel({
  session,
  usage,
  open,
  page = "root",
  variant = "",
  onClose,
  onPage,
  mcpServers,
  artifacts,
  facts: factList,
  inline = false,
  style,
}) {
  const panelRef = useRef(null);
  const sub = page !== "root";
  const close = onClose || closeSessionPanel;
  const goPage = onPage || setSessionPanelPage;

  useEffect(() => {
    if (!open) return undefined;
    const unregister = inline ? () => {} : registerOverlay("session-panel");
    const onKey = (event) => {
      if (event.key !== "Escape") return;
      event.stopPropagation();
      if (sub) goPage("root");
      else close();
    };
    document.addEventListener("keydown", onKey);
    return () => {
      unregister();
      document.removeEventListener("keydown", onKey);
    };
  }, [open, sub, inline]);

  if (!session) return null;

  const isOwner = session.kind === "owner";
  const facts = factList || runFacts(session);
  const mcp = mcpVerdict(session);
  const usageRow = usageVerdict(session, usage);
  const sheet = variant === "sheet";

  return (
    <aside
      ref={panelRef}
      class={`zl-side zl-side-right${open ? " is-open" : ""}${sheet ? " is-sheet" : ""}`}
      role="dialog"
      aria-label={isOwner ? "This owner" : "This session"}
      aria-hidden={!open}
      /* Closed it is slid off-screen, not gone: its Close, its name field,
         Usage and Delete stay in the DOM, and aria-hidden removes them from
         the accessibility TREE but not from the tab order -- so tabbing across
         the conversation landed the focus on controls nobody can see.
         inert is the narrow tool for it: focus and hit-testing only, nothing
         about painting. visibility:hidden also worked and was wrong, because
         it killed the panel's edge shadow and shifted every desktop scene. */
      inert={!open}
      style={style}
    >
      <div class={`zl-side-head${sub ? " is-sub" : ""}`}>
        {sub ? (
          <>
            <button type="button" class="zl-back" onClick={() => goPage("root")} aria-label="Back to this session">
              <BackIcon />
            </button>
            <span class="zl-side-title is-page" key={page}>{PANEL_PAGES[page]}</span>
          </>
        ) : (
          <>
            {isOwner && <OwnerPanelAvatar session={session} />}
            <span class="zl-side-title is-eyebrow">{isOwner ? "This owner" : "This session"}</span>
          </>
        )}
        <button type="button" class="zl-x" onClick={close} aria-label="Close">
          <svg viewBox="0 0 16 16" aria-hidden="true">
            <path d="M4 4l8 8M12 4l-8 8" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" />
          </svg>
        </button>
      </div>

      {sub ? (
        <div class="zl-panel-body is-sub" key={page}>
          {page === "usage" && (
            <UsagePage
              session={session}
              usage={usage}
              ctxPercent={session.contextPercent}
              costUSD={session.costUSD}
            />
          )}
          {page === "mcp" && (
            <McpPage sessionId={session.id} mcpTick={session.mcpTick} servers={mcpServers} inline={inline} />
          )}
          {(page === "overview" || page === "book") && <OwnerPanelPage session={session} page={page} phone={variant === "sheet"} />}
        </div>
      ) : (
        <>
          <div class="zl-panel-body">
            {!isOwner && <SessionIdentity session={session} inline={inline} />}
            {facts.length > 0 && (
              <dl class="zl-facts is-run">
                {facts.map((fact) => (
                  <div key={fact.id}>
                    <dt>{fact.label}</dt>
                    <dd class="zl-data">{fact.value}</dd>
                  </div>
                ))}
              </dl>
            )}
            <div class="zl-prows">
              {isOwner && <PanelRow id="overview" title="Overview" onOpen={goPage} />}
              {isOwner && <PanelRow id="book" title="Book" onOpen={goPage} />}
              <PanelRow
                id="usage"
                title="Usage"
                verdict={usageRow.text}
                warn={usageRow.warn}
                tone={usageRow.tone}
                onOpen={goPage}
              />
              {mcp && (
                <PanelRow id="mcp" title="MCP" verdict={mcp.text} warn={mcp.warn} onOpen={goPage} />
              )}
              <ArtifactsRow sessionId={session.id} items={artifacts} />
            </div>
          </div>
          {!isOwner && <LifecycleActions session={session} inline={inline} />}
        </>
      )}
    </aside>
  );
}

// The dossier's Artifacts row is a door to the drawer, not to a second list.
// There was a page here that listed the same files in a different shape, and
// picking one of them opened the drawer on its list anyway -- your choice was
// thrown away. One list, the designed one, reached from everywhere.
//
// The row still loads the collection to say how many files there are, with
// the claim view:'panel', which opens nothing. Without it the row said "none
// yet" for a session with 88 files, because nobody asked until you entered.
function ArtifactsRow({ sessionId, items }) {
  const slice = useStore(artifactsSlice);
  const mine = slice.ownerSessionId === sessionId;
  const count = items ? items.length : (mine ? slice.items.length : 0);
  const status = items ? "ready" : (mine ? slice.status : "idle");
  useEffect(() => {
    if (items || !sessionId) return;
    if (mine && slice.status !== "idle") return;
    listArtifactsInPanel(sessionId);
  }, [sessionId]);
  const verdict = artifactsVerdict(count, status);
  return (
    <PanelRow
      id="artifacts"
      title="Artifacts"
      verdict={verdict.text}
      warn={verdict.warn}
      onOpen={() => { if (sessionId) openArtifactsList(sessionId); }}
    />
  );
}

function SessionIdentity({ session, inline }) {
  const inputRef = useRef(null);
  const title = sessionTitle(session);
  const path = splitPath(session.cwd);

  const commit = (event) => {
    if (inline) return;
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

  // Date.now(), not `new Date()`, so a frozen lab clock (fidelity-freeze
  // patches Date.now only) still counts as "today".
  const startedAt = session.created ? new Date(session.created) : null;
  const started = startedAt && Number.isFinite(startedAt.getTime())
    ? (() => {
      const time = startedAt.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
      const today = new Date(Date.now()).toDateString() === startedAt.toDateString();
      return today ? time : `${startedAt.toLocaleDateString([], { day: "numeric", month: "short" })} ${time}`;
    })()
    : "";

  return (
    <>
      <label class="zl-field">
        <span class="zl-label">Name</span>
        <input
          class="zl-input"
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
      <div class="zl-field">
        <span class="zl-label">Folder</span>
        <div class="zl-input is-static zl-data">
          {path.dir && <span class="zl-path-dir">{path.dir}</span>}
          {path.base}
        </div>
        {started && (
          <div class="zl-field-meta zl-data">
            {session.worktree ? `${session.worktree} · ` : ""}started {started}
          </div>
        )}
      </div>
    </>
  );
}
