import { createPortal } from 'preact/compat';
import { useEffect, useLayoutEffect, useRef, useState } from 'preact/hooks';
import { ArrowLeft, ChevronLeft, ChevronRight, Layers, Loader2, Maximize2, Minimize2, Search, X } from 'lucide-preact';
import { useStore } from '../../hooks/useStore.js';
import { usePresence } from '../../hooks/usePresence.js';
import { MOTION } from '../../hooks/motion.js';
import { isTopLayer, pushLayer } from '../../data/overlay-layers.js';
import { registerOverlay } from '../../data/overlays.js';
import {
  artifactsOrigin, artifactsSlice, backToArtifactsList, closeArtifacts, moveArtifact, openArtifactFromList,
  restoreArtifactsFocus, retryArtifacts, setArtifactExpanded,
} from '../../data/artifacts.js';
import { artifactPosition, artifactsViewIsOpen, currentArtifact, filterArtifacts, originLabel } from '../../data/artifacts-model.js';
import { Field } from '../../primitives/index.js';
import { ArtifactRow, KindIcon, ShareButton } from './ArtifactRow.jsx';
import { ArtifactContent, ARTIFACT_ESCAPE, ARTIFACT_SWIPE } from './ArtifactContent.jsx';
import { groupArtifactsByDay } from './artifact-groups.js';
import './Artifacts.css';

// The same focusable set the Sheet traps on, plus the reader's iframe: an HTML
// artifact is part of the dialog, so Tab must reach it instead of stepping
// behind the panel.
const FOCUSABLE_SELECTOR =
  'a[href], button:not([disabled]), textarea:not([disabled]), input:not([disabled]), select:not([disabled]), iframe, [tabindex]:not([tabindex="-1"])';

// The app roots that must stop taking focus while the drawer is modal. The
// drawer is portalled to <body>, so these are its siblings, not its ancestors;
// marking them (and not <body>) leaves a Sheet opened ON TOP of the drawer —
// HtmlResourceInfo — fully interactive, since it is portalled to body too.
const BACKGROUND_ROOTS = '#root, .desktop-shell, .mconv';

function focusableIn(node) {
  if (!node) return [];
  return Array.from(node.querySelectorAll(FOCUSABLE_SELECTOR))
    .filter((el) => el.offsetParent !== null || el === document.activeElement);
}

function swipeDirection(start, end) {
  if (!start || !end) return null;
  const horizontal = end.x - start.x;
  const vertical = end.y - start.y;
  if (Math.abs(horizontal) < 72 || Math.abs(vertical) > 40 || Math.abs(horizontal) <= Math.abs(vertical)) return null;
  return horizontal < 0 ? 1 : -1;
}

// OriginName — the conversation that owns the drawer, inside the SAME single
// header row: a quiet, truncated name after the title, not a second bar. On a
// narrow phone CSS hides it visually; the accessible name above still carries
// it, so the origin is never lost.
function OriginName({ origin }) {
  if (!origin) return null;
  return <span class="af-head-origin" title={origin}>{origin}</span>;
}

// ArtifactsDrawer — ONE shared right-hand drawer, mounted once globally next to
// the palette. The conversation it shows is the one whose entry was clicked
// (slice.ownerSessionId); changing focus never switches it. Expanding is modal,
// and mobile is a full-screen modal.
export function ArtifactsDrawer() {
  const slice = useStore(artifactsSlice);
  const isMobile = useStore((s) => s.isMobile);
  // A plain string snapshot: naming the origin must not re-render the drawer on
  // every streamed token of the conversation it names.
  const origin = useStore(artifactsOrigin);
  const panel = useRef(null);
  const searchButton = useRef(null);
  const searchWasOpen = useRef(false);
  const [searching, setSearching] = useState(false);
  const [query, setQuery] = useState('');

  // 'panel' is a claim, not a door: the dossier's own artifacts page needs the
  // collection loaded under its own token without this drawer sliding over it.
  // The head entry asks the same question, so the answer lives in one place.
  const open = artifactsViewIsOpen(slice.view);
  const presence = usePresence(open, isMobile ? MOTION.exitBase : MOTION.exitFast);
  const list = slice.view === 'list';
  const fromList = slice.view === 'reader' && slice.from === 'list';
  const modal = isMobile || slice.expanded;
  const artifact = currentArtifact(slice);
  const position = artifactPosition(slice.items, slice.fileId);
  const swipeStart = useRef(null);
  const activeTouchPointers = useRef(new Set());

  useEffect(() => { if (!list) { setSearching(false); setQuery(''); } }, [list]);
  useEffect(() => { setQuery(''); }, [slice.ownerSessionId]);

  // Dismissing the inline search returns focus to the button that revealed it.
  useLayoutEffect(() => {
    if (list && !searching && searchWasOpen.current) {
      setQuery('');
      searchButton.current?.focus();
    }
    searchWasOpen.current = searching;
  }, [list, searching]);

  // Layer claim, for Escape ownership only (data/overlay-layers.js): one entry
  // for the drawer, plus a second while a reader opened FROM the list is
  // showing, so the drawer's capture-phase key handler below knows which of its
  // two surfaces the key belongs to and defers to any Sheet opened above it.
  // Nothing here touches browser history — closing is the X, Escape, the
  // backdrop or the reader's own Back.
  useEffect(() => {
    if (!open) return undefined;
    return pushLayer('artifacts');
  }, [open]);
  useEffect(() => {
    if (!fromList) return undefined;
    return pushLayer('artifact-reader');
  }, [fromList]);

  // While modal, the drawer is the top layer: global chords defer to it.
  useEffect(() => (modal && open ? registerOverlay('artifacts') : undefined), [modal, open]);

  useEffect(() => {
    if (!open) return undefined;
    const dismiss = () => {
      if (searching && list) setSearching(false);
      else if (fromList) backToArtifactsList();
      else closeArtifacts();
    };
    // The drawer's topmost layer id: the reader opened from the list claims
    // its own layer, so that is what must be on top for the key to be ours.
    const ownId = () => (fromList ? 'artifact-reader' : 'artifacts');
    const onKey = (event) => {
      if (event.key === 'Escape') {
        // This handler is on capture, so it also sees keys aimed at overlays
        // ABOVE the drawer (an HtmlResourceInfo Sheet opened from a row).
        // Those own their Escape; only act when the drawer is the top layer.
        if (!isTopLayer(ownId())) return;
        event.preventDefault();
        event.stopPropagation();
        dismiss();
        return;
      }
      const editable = event.target instanceof HTMLElement && event.target.closest('input, textarea, select, [contenteditable="true"]');
      if (slice.view === 'reader' && panel.current?.contains(event.target) && !editable && !event.altKey && !event.ctrlKey && !event.metaKey && !event.shiftKey && isTopLayer(ownId()) && (event.key === 'ArrowLeft' || event.key === 'ArrowRight')) {
        // Reader content has no editable fields. Keeping arrows on the drawer
        // means comparison works from the keyboard without stealing movement
        // inside an embedded HTML document, whose events do not leave its frame.
        if (moveArtifact(event.key === 'ArrowRight' ? 1 : -1)) event.preventDefault();
        return;
      }
      if (event.key !== 'Tab' || !modal || !isTopLayer(ownId())) return;
      // Modal means the dialog owns the tab ring: wrap at both ends instead of
      // letting Tab walk into the (inert) app behind it.
      const nodes = focusableIn(panel.current);
      if (nodes.length === 0) {
        event.preventDefault();
        panel.current?.focus();
        return;
      }
      const first = nodes[0];
      const last = nodes[nodes.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };
    // An Escape pressed inside the sandboxed HTML document arrives as a
    // message; only the drawer's OWN iframe may drive this.
    const onMessage = (event) => {
      const frame = panel.current?.querySelector('iframe');
      if (!frame || event.source !== frame.contentWindow) return;
      if (event.data?.type === ARTIFACT_ESCAPE) dismiss();
      if (event.data?.type === ARTIFACT_SWIPE) {
        const direction = swipeDirection(event.data.start, event.data.end);
        if (direction !== null) moveArtifact(direction);
      }
    };
    document.addEventListener('keydown', onKey, true);
    window.addEventListener('message', onMessage);
    return () => {
      document.removeEventListener('keydown', onKey, true);
      window.removeEventListener('message', onMessage);
    };
  }, [open, list, fromList, searching, modal, slice.view]);

  // Modal (mobile, or expanded on desktop): the app behind the dialog stops
  // taking focus, and the panel takes it. A NON-modal desktop drawer does none
  // of this on purpose — the conversation and its composer stay operable.
  useEffect(() => {
    if (!open || !modal || typeof document === 'undefined') return undefined;
    const background = Array.from(document.querySelectorAll(BACKGROUND_ROOTS))
      .filter((node) => !node.contains(panel.current) && !node.inert);
    for (const node of background) node.inert = true;
    return () => { for (const node of background) node.inert = false; };
  }, [open, modal, slice.view]);

  // Initial focus lands inside the dialog when it opens or changes view.
  // Toggling search must not steal focus from the field it just revealed.
  useEffect(() => {
    if (!open || !modal) return undefined;
    const node = panel.current;
    if (!node) return undefined;
    (focusableIn(node)[0] || node).focus?.({ preventScroll: true });
    return undefined;
  }, [open, modal, slice.view]);

  // Closing restores focus to whatever opened the drawer (head entry, pane
  // button or stream card), so the conversation is where the user left it.
  // Drafts and scroll are untouched: the drawer never unmounts a conversation.
  const wasOpen = useRef(false);
  useEffect(() => {
    if (!open && wasOpen.current) restoreArtifactsFocus();
    wasOpen.current = open;
  }, [open]);

  // Side-by-side vs overlay is decided in CSS from these flags: a wide desktop
  // reserves room for the drawer, a narrow viewport lets it overlay instead of
  // squeezing the conversation to a negative width. `artifacts-modal` also
  // lifts a Sheet opened FROM the drawer (HtmlResourceInfo) above it, since the
  // drawer's own modal layer sits above the normal sheet layer.
  useEffect(() => {
    if (typeof document === 'undefined') return undefined;
    const root = document.documentElement;
    root.classList.toggle('artifacts-open', open && !modal);
    root.classList.toggle('artifacts-modal', open && modal);
    return () => {
      root.classList.remove('artifacts-open');
      root.classList.remove('artifacts-modal');
    };
  }, [open, modal]);

  if (!presence.mounted || typeof document === 'undefined') return null;

  const items = slice.items;
  const filtered = filterArtifacts(items, query);
  const back = () => (fromList ? backToArtifactsList() : closeArtifacts());
  const onPointerDown = (event) => {
    if (event.pointerType !== 'touch' || event.target.closest('.af-image-wrap.is-zoomed')) return;
    activeTouchPointers.current.add(event.pointerId);
    // A pinch belongs to the image zoomer, never to collection navigation.
    if (activeTouchPointers.current.size !== 1) {
      swipeStart.current = null;
      return;
    }
    swipeStart.current = { id: event.pointerId, x: event.clientX, y: event.clientY };
  };
  const onPointerUp = (event) => {
    const start = swipeStart.current;
    activeTouchPointers.current.delete(event.pointerId);
    swipeStart.current = null;
    if (!start || start.id !== event.pointerId || event.pointerType !== 'touch' || event.target.closest('.af-image-wrap.is-zoomed')) return;
    const direction = swipeDirection(start, { x: event.clientX, y: event.clientY });
    if (direction !== null) moveArtifact(direction);
  };

  const panelNode = (
    <aside
      ref={panel}
      tabIndex={modal ? -1 : undefined}
      class={`af-drawer af-drawer-${slice.view}${isMobile ? ' is-mobile' : ''}${slice.expanded ? ' is-expanded' : ''}${presence.leaving ? ' is-leaving' : ''}`}
      role={modal ? 'dialog' : 'region'}
      aria-modal={modal || undefined}
      aria-label={originLabel(origin, list ? 'Artifacts' : artifact?.title || 'Artifact')}
    >
      <header class="af-head">
        <button
          type="button"
          class="af-back"
          onClick={list ? closeArtifacts : back}
          aria-label={list ? 'Back to the conversation' : fromList ? 'Back to artifacts' : 'Back to the conversation'}
        >
          <ArrowLeft size={18} />
        </button>
        {list ? (
          <span class="af-head-title">
            <span class="af-head-name">Artifacts</span>
            {items.length > 0 && <span class="af-head-count">{items.length}</span>}
            <OriginName origin={origin} />
          </span>
        ) : artifact && (
          <span class="af-head-title af-head-file">
            <button
              type="button"
              class="af-icon-button af-nav-button"
              onClick={() => moveArtifact(-1)}
              disabled={!position || position.index === 0}
              aria-label="Previous artifact"
            >
              <ChevronLeft size={17} />
            </button>
            <KindIcon artifact={artifact} size={14} />
            <span class="af-head-name">{artifact.name}</span>
            {position && <output class="af-head-position" aria-live="polite">{position.index + 1} of {position.total}</output>}
            <button
              type="button"
              class="af-icon-button af-nav-button"
              onClick={() => moveArtifact(1)}
              disabled={(!position || position.index === position.total - 1)}
              aria-label="Next artifact"
            >
              <ChevronRight size={17} />
            </button>
            <OriginName origin={origin} />
          </span>
        )}
        <div class="af-head-actions">
          {list && items.length > 0 && (
            <button
              ref={searchButton}
              type="button"
              class={`af-icon-button${searching ? ' is-on' : ''}`}
              onClick={() => setSearching((value) => !value)}
              aria-label="Search artifacts"
              aria-expanded={searching}
            >
              <Search size={17} />
            </button>
          )}
          {!list && artifact && <ShareButton artifact={artifact} labelled={!isMobile} />}
          {!isMobile && !list && (
            <button
              type="button"
              class="af-icon-button"
              onClick={() => setArtifactExpanded(!slice.expanded)}
              aria-label={slice.expanded ? 'Exit full screen' : 'Expand artifact'}
              aria-pressed={slice.expanded}
            >
              {slice.expanded ? <Minimize2 size={16} /> : <Maximize2 size={16} />}
            </button>
          )}
          <button type="button" class="af-icon-button" aria-label="Close artifacts" onClick={closeArtifacts}>
            <X size={18} />
          </button>
        </div>
      </header>

      {list ? (
        <div class="af-list-content">
          {searching && (
            <Field
              variant="box"
              size="lg"
              class="af-search"
              leading={<Search size={16} />}
              trailing={query ? (
                <button type="button" class="af-icon-button" aria-label="Clear search" onClick={() => setQuery('')}>
                  <X size={14} />
                </button>
              ) : null}
              autoFocus
              aria-label="Search artifacts"
              placeholder="Search by title or file name…"
              value={query}
              onInput={(event) => setQuery(event.currentTarget.value)}
            />
          )}
          <ArtifactListBody slice={slice} items={items} filtered={filtered} onClearSearch={() => { setQuery(''); setSearching(false); }} />
        </div>
      ) : artifact ? (
        <div class="af-document-body" onPointerDown={onPointerDown} onPointerUp={onPointerUp} onPointerCancel={(event) => { activeTouchPointers.current.delete(event.pointerId); swipeStart.current = null; }}>
          <ArtifactContent key={artifact.id} artifact={artifact} />
        </div>
      ) : slice.status === 'loading' ? (
        <div class="af-loading" role="status"><Loader2 class="spin" size={16} /> Opening…</div>
      ) : (
        <div class="af-empty" role="alert">
          <h2>Not in this conversation</h2>
          <p>This file is no longer here.</p>
          <button type="button" class="af-text-button" onClick={backToArtifactsList}>See all artifacts</button>
        </div>
      )}
    </aside>
  );

  return createPortal(
    <div class={`af-layer${modal ? ' is-modal' : ''}${presence.leaving ? ' is-leaving' : ''}`}>
      {modal && <div class={`af-backdrop${presence.leaving ? ' is-leaving' : ''}`} onClick={closeArtifacts} aria-hidden="true" />}
      {panelNode}
    </div>,
    document.body,
  );
}

function ArtifactListBody({ slice, items, filtered, onClearSearch }) {
  if (slice.status === 'loading' && items.length === 0) {
    return <div class="af-loading" role="status"><Loader2 class="spin" size={16} /> Loading artifacts…</div>;
  }
  if (slice.status === 'error' && items.length === 0) {
    return (
      <div class="af-empty" role="alert">
        <h2>Could not load artifacts</h2>
        <p>The collection could not be read.</p>
        <button type="button" class="af-text-button" onClick={retryArtifacts}>Retry</button>
      </div>
    );
  }
  if (filtered.length === 0) {
    return (
      <div class="af-empty">
        <Layers size={28} aria-hidden="true" />
        <h2>{items.length ? 'No matches' : 'No artifacts yet'}</h2>
        <p>{items.length ? 'Try another title or file name.' : 'Ask the agent to send you a file.'}</p>
        <button type="button" class="af-text-button" onClick={items.length ? onClearSearch : closeArtifacts}>
          {items.length ? 'Show all artifacts' : 'Back to the conversation'}
        </button>
      </div>
    );
  }
  // Day headings give the pile a rhythm; a search result is already a
  // selection, so it stays flat rather than scattering three hits over three
  // headings.
  const groups = filtered === items ? groupArtifactsByDay(items) : [{ label: '', items: filtered }];
  return (
    <div class="af-groups">
      {groups.map((group) => (
        <section key={group.label || 'results'} class="af-group">
          {group.label && (
            <h3 class="af-group-head">
              <span>{group.label}</span>
              <span class="af-group-n">{group.items.length}</span>
            </h3>
          )}
          <ul class="af-list">
            {group.items.map((entry) => (
              <li key={entry.id}>
                <ArtifactRow artifact={entry} onOpen={(a) => openArtifactFromList(a.id)} />
              </li>
            ))}
          </ul>
        </section>
      ))}
    </div>
  );
}
