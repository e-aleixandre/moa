import { useState, useRef, useEffect, useMemo, useCallback } from 'preact/hooks';
import { Plus, Search, CornerDownLeft } from 'lucide-preact';
import { assignToTile } from '../tile-actions.js';
import { resumeSession, unarchiveSession } from '../session-actions.js';
import { allTileIds, findTile } from '../tileTree.js';
import { addToast } from '../notifications.js';
import { sessionDotState, isRecentSession } from '../util/format.js';
import { NewSessionSheet } from './NewSessionSheet.jsx';

// Cached capabilities from server
let _caps = null;
function getCaps() {
  if (_caps) return Promise.resolve(_caps);
  return fetch('/api/capabilities', { headers: { 'X-Moa-Request': '1' } })
    .then(r => r.json())
    .then(c => { _caps = c; return c; })
    .catch(() => ({}));
}

export function CommandPalette({ open, onClose, state, initialMode = 'search', onOpenPairing }) {
  const [query, setQuery] = useState('');
  const [selectedIdx, setSelectedIdx] = useState(0);
  const [mode, setMode] = useState(initialMode);
  const [serverCwd, setServerCwd] = useState('');
  const [homeDir, setHomeDir] = useState('');
  const [defaultModel, setDefaultModel] = useState('');
  const inputRef = useRef(null);
  const listRef = useRef(null);

  // Fetch capabilities on first open
  useEffect(() => {
    if (open) {
      getCaps().then(c => {
        if (c.workspaceRoot) setServerCwd(c.workspaceRoot);
        if (c.homeDir) setHomeDir(c.homeDir);
        if (c.defaultModel) setDefaultModel(c.defaultModel);
      });
    }
  }, [open]);

  // Reset on open
  useEffect(() => {
    if (open) {
      setQuery('');
      setSelectedIdx(0);
      setMode(initialMode);
      setCreating(false);
      requestAnimationFrame(() => inputRef.current?.focus());
    }
  }, [open, initialMode]);

  // Re-focus input on mode change
  useEffect(() => {
    if (open) {
      setQuery('');
      setSelectedIdx(0);
      requestAnimationFrame(() => inputRef.current?.focus());
    }
  }, [mode]);

  // --- SEARCH MODE: session list ---
  const searchItems = useMemo(() => {
    if (mode !== 'search') return [];
    const result = [];

    result.push({ type: 'action', id: '__new', label: 'New session…' });
    result.push({ type: 'action', id: '__pair-pulse', label: 'Pair Pulse…' });

    const q = query.toLowerCase().trim();
    // Order by most-recently-used first (consistent with the mobile dashboard),
    // not grouped by project. With no query, hide archived ("closed") sessions
    // and anything older than the recent window to keep the list scannable; a
    // query searches everything so old sessions stay findable.
    const sessions = Object.values(state.sessions)
      .sort((a, b) => (b.updated || 0) - (a.updated || 0));
    for (const sess of sessions) {
      const cwd = sess.cwd || '';
      const cwdLabel = cwd.split('/').pop() || cwd;
      if (q) {
        const haystack = `${sess.title || ''} ${sess.model || ''} ${cwdLabel} ${cwd}`.toLowerCase();
        if (!fuzzyMatch(q, haystack)) continue;
      } else {
        if (sess.archived) continue;
        if (!isRecentSession(sess)) continue;
      }
      const ids = allTileIds(state.tileTree);
      let inTile = null;
      for (const tid of ids) {
        const t = findTile(state.tileTree, tid);
        if (t && t.sessionId === sess.id) {
          inTile = ids.indexOf(tid) + 1;
          break;
        }
      }
      result.push({
        type: 'session', id: sess.id,
        title: sess.title || 'Untitled',
        model: sess.model, state: sess.state,
        dotState: sessionDotState(sess),
        cwd: cwdLabel, inTile,
        saved: sess.state === 'saved',
        archived: !!sess.archived,
      });
    }
    return result;
  }, [state.sessions, state.tileTree, query, mode]);

  const items = searchItems;

  // Clamp selection
  useEffect(() => {
    if (selectedIdx >= items.length) setSelectedIdx(Math.max(0, items.length - 1));
  }, [items.length, selectedIdx]);

  // Scroll selected into view
  useEffect(() => {
    const el = listRef.current?.children[selectedIdx];
    if (el) el.scrollIntoView({ block: 'nearest' });
  }, [selectedIdx]);

  const handleSelectSearch = useCallback(async (item) => {
    if (item.type === 'action' && item.id === '__new') {
      setMode('create');
      return;
    }
    if (item.type === 'action' && item.id === '__pair-pulse') {
      onClose();
      onOpenPairing();
      return;
    }
    if (item.type === 'session') {
      if (item.archived) {
        // Reopen: the server also auto-unarchives on resume/send, but for
        // sessions merely assigned to a tile (not resumed/sent-to), do it
        // explicitly so it doesn't linger flagged as archived.
        unarchiveSession(item.id).catch(e => console.error('Unarchive failed:', e));
      }
      if (item.saved) {
        // Resume puts the session into the focused tile (see state.resumeSession).
        try {
          await resumeSession(item.id);
        } catch (e) {
          addToast(`Could not resume session: ${e.message}`, 'error');
          return;
        }
      } else {
        // Always assign to the focused tile. If the session was visible
        // elsewhere, assignToTile clears it from the old tile first.
        assignToTile(state.focusedTile, item.id);
      }
      onClose();
    }
  }, [state.focusedTile, onClose, onOpenPairing]);

  const handleSelect = useCallback((item) => {
    return handleSelectSearch(item);
  }, [handleSelectSearch]);

  const handleKeyDown = useCallback((e) => {
    switch (e.key) {
      case 'ArrowDown':
        e.preventDefault();
        setSelectedIdx(i => Math.min(i + 1, items.length - 1));
        break;
      case 'ArrowUp':
        e.preventDefault();
        setSelectedIdx(i => Math.max(i - 1, 0));
        break;
      case 'Enter':
        e.preventDefault();
        if (items[selectedIdx]) handleSelect(items[selectedIdx]);
        break;
      case 'Escape':
        e.preventDefault();
        onClose();
        break;
    }
  }, [items, selectedIdx, handleSelect, onClose]);

  if (!open) return null;

  // Create mode is a full flow of its own (recents + filesystem browser), so it
  // renders the dedicated NewSessionSheet. The onBack returns to the session
  // search list (desktop ⌘K enters search first); on mobile the overview opens
  // create directly, so back closes.
  if (mode === 'create') {
    return (
      <div class="palette-overlay ns-overlay" onClick={(e) => { if (e.target === e.currentTarget) onClose(); }}>
        <NewSessionSheet
          state={state}
          serverCwd={serverCwd}
          homeDir={homeDir}
          defaultModel={defaultModel}
          onClose={onClose}
          onBack={initialMode === 'search' ? () => setMode('search') : onClose}
        />
      </div>
    );
  }

  const placeholder = 'Search sessions…';

  return (
    <div class="palette-overlay" onClick={(e) => { if (e.target === e.currentTarget) onClose(); }}>
      <div class="palette" onKeyDown={handleKeyDown}>
        <div class="palette-input-row">
          <Search class="palette-search-icon" />
          <input
            ref={inputRef}
            class="palette-input"
            type="text"
            placeholder={placeholder}
            value={query}
            onInput={(e) => { setQuery(e.target.value); setSelectedIdx(0); }}
          />
          <kbd class="shortcut-hint palette-esc">esc</kbd>
        </div>
        <div class="palette-list" ref={listRef}>
          {searchItems.map((item, i) => (
            item.type === 'action' ? (
              <div
                key={item.id}
                class={`palette-item palette-action ${i === selectedIdx ? 'selected' : ''}`}
                onClick={() => handleSelect(item)}
                onMouseEnter={() => setSelectedIdx(i)}
              >
                <Plus class="palette-item-icon" />
                <span class="palette-item-label">{item.label}</span>
                {i === selectedIdx && <CornerDownLeft class="palette-enter-hint" />}
              </div>
            ) : (
              <div
                key={item.id}
                class={`palette-item ${i === selectedIdx ? 'selected' : ''} ${item.saved ? 'saved' : ''}`}
                onClick={() => handleSelect(item)}
                onMouseEnter={() => setSelectedIdx(i)}
              >
                <span class={`state-dot ${item.dotState || item.state}`} />
                <span class="palette-item-label">{item.title}</span>
                {item.archived && <span class="palette-archived-badge">archived</span>}
                <span class="palette-item-meta">{item.model}</span>
                <span class="palette-item-cwd">{item.cwd}</span>
                {item.inTile && <span class="palette-tile-badge">{item.inTile}</span>}
                {i === selectedIdx && <CornerDownLeft class="palette-enter-hint" />}
              </div>
            )
          ))}
          {items.length === 0 && (
            <div class="palette-empty">No matching sessions</div>
          )}
        </div>
      </div>
    </div>
  );
}

function fuzzyMatch(query, haystack) {
  let qi = 0;
  for (let i = 0; i < haystack.length && qi < query.length; i++) {
    if (haystack[i] === query[qi]) qi++;
  }
  return qi === query.length;
}

