import { useState } from "preact/hooks";
import { Dot, SessionRow } from "../components/SessionRow/SessionRow.jsx";
import { projectName } from "../data/util/format.js";
import {
  attentionKind,
  groupProjectSessions,
  partitionByAttention,
  projectCollapsed,
} from "../data/util/project-sessions.js";
import { OwnerRow, SectionHead } from "../components/Owners/OwnerRow.jsx";
import { worstOwnerState } from "../data/owners-model.js";
import "../layout/Sidebar/Sidebar.css";
import "../components/Owners/OwnerAvatar.css";
import "../components/Owners/OwnerRow.css";

/* The lab's sidebar HOST — the scene, not the pieces.

   The owner row, its collapsible section heading, the avatar and the identity
   picker are production's own files now (src/components/Owners/*): this page
   imports them rather than keeping copies, so a photograph taken here is a
   photograph of the shipped product (tmp/redesign/fidelity/METODO.md).

   What remains local is the FRAME: the real Sidebar reads the store, the
   roster and the persisted accordion, and this page has none of those. So it
   draws the same head, the same foot and the same list arrangement over plain
   props, which is what lets a preset switch between states the real column
   only reaches by having the work actually be in that state. */

/* ── The candidate sidebar ───────────────────────────────────────────────
   Production's Sidebar with iteration 3's list, drawn here because the shipped
   one cannot express it yet. Head and foot are the shipped markup (`zl-side-*`
   under Sidebar.css) and the session rows are the shipped SessionRow, so what
   is genuinely new is only the list's arrangement — which is the thing being
   decided. The segmented is back to TWO positions. */

/* The project group's chevron. The heading the lab draws for a PROJECT is the
   host's own (the real one is inside Sidebar.jsx, wired to the store), so it
   needs the same glyph; the SECTION headings come from production. */
function ChevronIcon() {
  return (
    <svg class="zl-proj-chev" viewBox="0 0 16 16" aria-hidden="true">
      <path d="M6 3.5L10.5 8 6 12.5" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" />
    </svg>
  );
}

function PlusIcon() {
  return (
    <svg viewBox="0 0 16 16" aria-hidden="true">
      <path d="M8 3.5v9M3.5 8h9" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" />
    </svg>
  );
}
function SearchIcon() {
  return (
    <svg class="zl-search-ico" viewBox="0 0 16 16" aria-hidden="true">
      <circle cx="7" cy="7" r="4.5" fill="none" stroke="currentColor" stroke-width="1.6" />
      <path d="M10.5 10.5L14 14" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" />
    </svg>
  );
}
function InboxIcon() {
  return (
    <svg viewBox="0 0 16 16" aria-hidden="true">
      <path d="M2 9.5V12a1 1 0 0 0 1 1h10a1 1 0 0 0 1-1V9.5M2 9.5h3.2l.8 1.5h4l.8-1.5H14M2 9.5l1.6-5.2A1 1 0 0 1 4.6 3.5h6.8a1 1 0 0 1 1 .8L14 9.5" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linejoin="round" />
    </svg>
  );
}
function GearIcon() {
  return (
    <svg viewBox="0 0 16 16" aria-hidden="true">
      <circle cx="8" cy="8" r="2.6" fill="none" stroke="currentColor" stroke-width="1.5" />
      <path d="M7.01 1.07 L8.99 1.07 L9.33 2.82 L10.72 3.40 L12.20 2.40 L13.60 3.80 L12.60 5.28 L13.18 6.67 L14.93 7.01 L14.93 8.99 L13.18 9.33 L12.60 10.72 L13.60 12.20 L12.20 13.60 L10.72 12.60 L9.33 13.18 L8.99 14.93 L7.01 14.93 L6.67 13.18 L5.28 12.60 L3.80 13.60 L2.40 12.20 L3.40 10.72 L2.82 9.33 L1.07 8.99 L1.07 7.01 L2.82 6.67 L3.40 5.28 L2.40 3.80 L3.80 2.40 L5.28 3.40 L6.67 2.82 Z" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linejoin="round" />
    </svg>
  );
}
function ByRecentIcon() {
  return (
    <svg viewBox="0 0 16 16" aria-hidden="true">
      <circle cx="8" cy="8" r="5.6" fill="none" stroke="currentColor" stroke-width="1.5" />
      <path d="M8 4.7V8l2.2 1.5" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />
    </svg>
  );
}
function ByProjectIcon() {
  return (
    <svg viewBox="0 0 16 16" aria-hidden="true">
      <path d="M1.9 5.1V3.4a.9.9 0 0 1 .9-.9h2.4l1.2 1.4h4.7a.9.9 0 0 1 .9.9v.3" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" />
      <rect x="1.9" y="6.2" width="12.2" height="7.3" rx="1" fill="none" stroke="currentColor" stroke-width="1.4" />
      <path d="M4.6 9.1h6.8M4.6 11.2h4.3" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" />
    </svg>
  );
}

// The segmented is TWO positions again. It says how the sessions are ORDERED;
// Owners is not an order, so it was never one of its options — that was the
// thing iteration 3 undid.
const ORDERS = [
  ["recent", "Recent", "Sort by recent", ByRecentIcon],
  ["project", "By project", "Group by project", ByProjectIcon],
];

export function OwnersSidebar({
  density = "desktop",
  version = { current: "v0.37.4" },
  mode = "recent",
  onMode,
  owners = [],
  sessions = [],
  saved = [],
  activeId = null,
  activeOwnerId = null,
  collapsed = {},
  onToggleSection,
  collapsedProjects = {},
  onToggleProject,
  onOpenOwner,
  onSelectSession,
  onNewSession,
  onSearch,
  onSettings,
  inboxCount = 2,
  onInbox,
  children,
}) {
  const phone = density === "phone";
  const { needs, rest } = partitionByAttention(sessions);
  const sectionOpen = (key) => collapsed[key] !== true;
  const ownersDot = worstOwnerState(owners);

  const row = (s, underProject = false) => (
    <SessionRow
      key={s.id}
      title={s.title}
      state={s.state || (s.saved ? "saved" : "idle")}
      active={s.id === activeId}
      unseen={s.unseen}
      when={s.when}
      brief={s.brief}
      briefTone={s.briefTone}
      path={s.brief ? undefined : s.path}
      project={s.brief && !underProject ? projectName(s.cwd) || undefined : undefined}
      onClick={() => onSelectSession?.(s.id)}
    />
  );

  const projectSections = groupProjectSessions([...sessions, ...saved], owners);
  const ownerOfSection = (section) => (section.ownerId ? owners.find((o) => o.id === section.ownerId) || null : null);

  return (
    <aside class={`zl-side-body${phone ? " is-phone" : ""}`}>
      <div class="zl-side-head">
        <span class="zl-side-title">moa</span>
        <button type="button" class="zl-search is-door" onClick={onSearch} aria-label="Search" title="Search">
          <SearchIcon />
        </button>
        <button type="button" class="zl-side-add" onClick={onNewSession} title="New session" aria-label="New session">
          <PlusIcon />
        </button>
        <div class={`zl-view is-${mode}`} role="radiogroup" aria-label="Session order">
          {ORDERS.map(([id, label, hint, Icon]) => (
            <button
              type="button"
              role="radio"
              aria-checked={id === mode}
              aria-label={hint}
              title={label}
              class={`zl-view-b${id === mode ? " is-on" : ""}`}
              onClick={() => onMode?.(id)}
              key={id}
            >
              <Icon />
            </button>
          ))}
        </div>
      </div>

      {children || (
        <div class="zl-list">
          {mode === "project" ? projectSections.map((section) => {
            const open = !projectCollapsed(section, collapsedProjects, false);
            const owner = ownerOfSection(section);
            const name = projectName(section.key) || section.label;
            const worst = section.sessions.map(attentionKind).filter(Boolean)[0] || null;
            return (
              <div class={`zl-proj${open ? " is-open" : ""}`} key={section.key}>
                <button
                  type="button"
                  class="zl-group is-proj"
                  aria-expanded={open}
                  aria-label={`${section.label}, ${section.openCount} open`}
                  onClick={() => onToggleProject?.(section.key, open)}
                >
                  <ChevronIcon />
                  <span>{name}</span>
                  {worst && <Dot state={worst} />}
                  <span class="zl-proj-path zl-data">{section.path}</span>
                  <span class="zl-group-n zl-data">{section.sessions.length}</span>
                </button>
                {open && (
                  <>
                    {/* The owner is the first row of its group. Not a section
                        of its own here: it belongs to this project, and the
                        heading has just named it. A project with no owner is
                        exactly what it is today. */}
                    {owner && (
                      <OwnerRow
                        owner={owner}
                        project
                        active={owner.id === activeOwnerId}
                        onOpen={onOpenOwner}
                      />
                    )}
                    {section.sessions.map((s) => row(s, true))}
                  </>
                )}
              </div>
            );
          }) : (
            <>
              {/* OWNERS first, above everything. They are what the sessions
                  below belong TO, and they are two or three rows: a section
                  that never grows can afford the top of the column. */}
              {owners.length > 0 && (
                <>
                  <SectionHead
                    label="Owners"
                    n={owners.length}
                    open={sectionOpen("owners")}
                    dot={ownersDot}
                    onToggle={() => onToggleSection?.("owners")}
                  />
                  {sectionOpen("owners") && owners.map((owner) => (
                    <OwnerRow
                      key={owner.id}
                      owner={owner}
                      active={owner.id === activeOwnerId}
                      onOpen={onOpenOwner}
                    />
                  ))}
                </>
              )}
              {needs.length > 0 && (
                <>
                  <SectionHead label="Needs attention" n={needs.length} attn />
                  {needs.map((s) => row(s))}
                </>
              )}
              {rest.length > 0 && (
                <>
                  <SectionHead
                    label="Active"
                    n={rest.length}
                    open={sectionOpen("active")}
                    onToggle={() => onToggleSection?.("active")}
                  />
                  {sectionOpen("active") && rest.map((s) => row(s))}
                </>
              )}
              {saved.length > 0 && (
                <>
                  <SectionHead
                    label="Saved"
                    n={saved.length}
                    open={sectionOpen("saved")}
                    onToggle={() => onToggleSection?.("saved")}
                  />
                  {sectionOpen("saved") && saved.map((s) => row(s))}
                </>
              )}
            </>
          )}
        </div>
      )}

      <div class="zl-side-foot">
        <button type="button" class="zl-inbox" aria-label={`Inbox, ${inboxCount} waiting`} onClick={onInbox}>
          <InboxIcon />
          Inbox
          {inboxCount > 0 && <span class="zl-inbox-n zl-data">{inboxCount}</span>}
        </button>
        <div class="zl-side-app">
          <span class="zl-ver zl-data" title="moa version">{version.current}</span>
          <button type="button" class="zl-gear" aria-label="Settings" onClick={onSettings}>
            <GearIcon />
          </button>
        </div>
      </div>
    </aside>
  );
}


// A tiny controlled wrapper so the lab's own New owner form can mount the
// shipped picker without owning four pieces of state. It stays here because
// production's NewOwner keeps that state itself, beside the folder it follows.
export function useAvatarChoice(initial) {
  const [shape, setShape] = useState(initial.shape);
  const [color, setColor] = useState(initial.color);
  return { shape, color, setShape, setColor };
}
