import { createPortal } from 'preact/compat';
import { useEffect, useLayoutEffect, useRef, useState } from 'preact/hooks';
import { ArrowLeft, Layers, Loader2, Maximize2, Minimize2, Search, X } from 'lucide-preact';
import { useStore } from '../../hooks/useStore.js';
import { isTopOverlay, openOverlay } from '../../data/overlay-history.js';
import { registerOverlay } from '../../data/overlays.js';
import {
  artifactsOrigin, artifactsSlice, backToArtifactsList, closeArtifacts, openArtifactFromList,
  restoreArtifactsFocus, retryArtifacts, setArtifactExpanded,
} from '../../data/artifacts.js';
import { currentArtifact, filterArtifacts, originLabel } from '../../data/artifacts-model.js';
import { ArtifactRow, KindIcon, ShareButton } from './ArtifactRow.jsx';
import { ArtifactContent, ARTIFACT_ESCAPE } from './ArtifactContent.jsx';
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

  const open = !!slice.view;
  const list = slice.view === 'list';
  const fromList = slice.view === 'reader' && slice.from === 'list';
  const modal = isMobile || slice.expanded;
  const artifact = currentArtifact(slice);

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

  // Back gesture / browser Back: one entry for the whole drawer (the shared
  // overlay-history module), plus a second layer while a reader opened FROM the
  // list is showing, so Back returns to the list first. Cleanup closes through
  // the module (no fromPop flag) so the guard entry is consumed exactly like
  // every Sheet does; an already-popped entry makes it a no-op.
  useEffect(() => {
    if (!open) return undefined;
    const close = openOverlay('artifacts', () => closeArtifacts());
    return () => close();
  }, [open]);
  useEffect(() => {
    if (!fromList) return undefined;
    const close = openOverlay('artifact-reader', () => backToArtifactsList());
    return () => close();
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
    // The drawer's topmost overlay id: the reader opened from the list pushes
    // its own entry, so that is what must be on top for the key to be ours.
    const ownId = () => (fromList ? 'artifact-reader' : 'artifacts');
    const onKey = (event) => {
      if (event.key === 'Escape') {
        // This handler is on capture, so it also sees keys aimed at overlays
        // ABOVE the drawer (an HtmlResourceInfo Sheet opened from a row).
        // Those own their Escape; only act when the drawer is the top overlay.
        if (!isTopOverlay(ownId())) return;
        event.preventDefault();
        event.stopPropagation();
        dismiss();
        return;
      }
      if (event.key !== 'Tab' || !modal || !isTopOverlay(ownId())) return;
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
    };
    document.addEventListener('keydown', onKey, true);
    window.addEventListener('message', onMessage);
    return () => {
      document.removeEventListener('keydown', onKey, true);
      window.removeEventListener('message', onMessage);
    };
  }, [open, list, fromList, searching, modal]);

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
  }, [open, modal, slice.view, slice.fileId]);

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

  if (!open || typeof document === 'undefined') return null;

  const items = slice.items;
  const filtered = filterArtifacts(items, query);
  const back = () => (fromList ? backToArtifactsList() : closeArtifacts());

  const panelNode = (
    <aside
      ref={panel}
      tabIndex={modal ? -1 : undefined}
      class={`af-drawer af-drawer-${slice.view}${isMobile ? ' is-mobile' : ''}${slice.expanded ? ' is-expanded' : ''}`}
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
            <KindIcon artifact={artifact} size={14} />
            <span class="af-head-name">{artifact.name}</span>
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
            <div class="af-search">
              <Search size={16} aria-hidden="true" />
              <input
                autoFocus
                aria-label="Search artifacts"
                placeholder="Search by title or file name…"
                value={query}
                onInput={(event) => setQuery(event.currentTarget.value)}
              />
              {query && (
                <button type="button" class="af-icon-button" aria-label="Clear search" onClick={() => setQuery('')}>
                  <X size={14} />
                </button>
              )}
            </div>
          )}
          <ArtifactListBody slice={slice} items={items} filtered={filtered} onClearSearch={() => { setQuery(''); setSearching(false); }} />
        </div>
      ) : artifact ? (
        <div class="af-document-body">
          <ArtifactContent key={artifact.id} artifact={artifact} />
        </div>
      ) : slice.status === 'loading' ? (
        <div class="af-loading" role="status"><Loader2 class="spin" size={16} /> Opening…</div>
      ) : (
        <div class="af-empty" role="alert">
          <h2>Not in this conversation</h2>
          <p>This artifact is not in this collection any more.</p>
          <button type="button" class="af-text-button" onClick={backToArtifactsList}>See all artifacts</button>
        </div>
      )}
    </aside>
  );

  return createPortal(
    <div class={`af-layer${modal ? ' is-modal' : ''}`}>
      {modal && <div class="af-backdrop" onClick={closeArtifacts} aria-hidden="true" />}
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
  return (
    <ul class="af-list">
      {filtered.map((entry) => (
        <li key={entry.id}>
          <ArtifactRow artifact={entry} onOpen={(a) => openArtifactFromList(a.id)} />
        </li>
      ))}
    </ul>
  );
}
