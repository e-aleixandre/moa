import { useState, useRef, useEffect, useMemo, useCallback } from 'preact/hooks';
import { Plus, Search, ArrowRight, CornerDownLeft } from 'lucide-preact';
import {
  store, assignToTile, focusTile, resumeSession, sessionsByGroup,
  isSessionInTile, deleteSession,
} from '../state.js';
import { allTileIds, findTile } from '../tileTree.js';

/**
 * Command palette for session management.
 * ⌘K to open. Fuzzy search, assign to focused tile, create new session.
 */
export function CommandPalette({ open, onClose, onNewSession, state }) {
  const [query, setQuery] = useState('');
  const [selectedIdx, setSelectedIdx] = useState(0);
  const inputRef = useRef(null);
  const listRef = useRef(null);

  // Reset on open
  useEffect(() => {
    if (open) {
      setQuery('');
      setSelectedIdx(0);
      // Small delay to let the DOM mount
      requestAnimationFrame(() => inputRef.current?.focus());
    }
  }, [open]);

  // Build flat session list with fuzzy filtering
  const items = useMemo(() => {
    const groups = sessionsByGroup(state);
    const result = [];

    // "New session" is always first
    result.push({ type: 'action', id: '__new', label: 'New session…' });

    const q = query.toLowerCase().trim();

    for (const [cwd, sessions] of Object.entries(groups)) {
      const cwdLabel = cwd.split('/').pop() || cwd;
      for (const sess of sessions) {
        if (q) {
          const haystack = `${sess.title || ''} ${sess.model || ''} ${cwdLabel} ${cwd}`.toLowerCase();
          if (!fuzzyMatch(q, haystack)) continue;
        }
        // Find which tile this session is in (if any)
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
          type: 'session',
          id: sess.id,
          title: sess.title || 'Untitled',
          model: sess.model,
          state: sess.state,
          cwd: cwdLabel,
          inTile,
          saved: sess.state === 'saved',
        });
      }
    }

    return result;
  }, [state.sessions, state.tileTree, query]);

  // Clamp selection
  useEffect(() => {
    if (selectedIdx >= items.length) setSelectedIdx(Math.max(0, items.length - 1));
  }, [items.length, selectedIdx]);

  // Scroll selected into view
  useEffect(() => {
    const el = listRef.current?.children[selectedIdx];
    if (el) el.scrollIntoView({ block: 'nearest' });
  }, [selectedIdx]);

  const handleSelect = useCallback((item) => {
    if (item.type === 'action' && item.id === '__new') {
      onClose();
      onNewSession();
      return;
    }
    if (item.type === 'session') {
      if (item.saved) {
        resumeSession(item.id).catch(e => console.error('Resume failed:', e));
      } else if (item.inTile) {
        // Already in a tile — focus that tile
        const ids = allTileIds(state.tileTree);
        const tileId = (() => {
          for (const tid of ids) {
            const t = findTile(state.tileTree, tid);
            if (t && t.sessionId === item.id) return tid;
          }
          return null;
        })();
        if (tileId != null) focusTile(tileId);
      } else {
        assignToTile(state.focusedTile, item.id);
      }
      onClose();
    }
  }, [state.tileTree, state.focusedTile, onClose, onNewSession]);

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

  return (
    <div class="palette-overlay" onClick={(e) => { if (e.target === e.currentTarget) onClose(); }}>
      <div class="palette" onKeyDown={handleKeyDown}>
        <div class="palette-input-row">
          <Search class="palette-search-icon" />
          <input
            ref={inputRef}
            class="palette-input"
            type="text"
            placeholder="Search sessions…"
            value={query}
            onInput={(e) => { setQuery(e.target.value); setSelectedIdx(0); }}
          />
          <kbd class="shortcut-hint palette-esc">esc</kbd>
        </div>
        <div class="palette-list" ref={listRef}>
          {items.map((item, i) => {
            if (item.type === 'action') {
              return (
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
              );
            }
            return (
              <div
                key={item.id}
                class={`palette-item ${i === selectedIdx ? 'selected' : ''} ${item.saved ? 'saved' : ''}`}
                onClick={() => handleSelect(item)}
                onMouseEnter={() => setSelectedIdx(i)}
              >
                <span class={`state-dot ${item.state}`} />
                <span class="palette-item-label">{item.title}</span>
                <span class="palette-item-meta">{item.model}</span>
                <span class="palette-item-cwd">{item.cwd}</span>
                {item.inTile && <span class="palette-tile-badge">{item.inTile}</span>}
                {i === selectedIdx && <CornerDownLeft class="palette-enter-hint" />}
              </div>
            );
          })}
          {items.length === 0 && (
            <div class="palette-empty">No matching sessions</div>
          )}
        </div>
      </div>
    </div>
  );
}

/** Simple fuzzy match: all query chars appear in order in haystack */
function fuzzyMatch(query, haystack) {
  let qi = 0;
  for (let i = 0; i < haystack.length && qi < query.length; i++) {
    if (haystack[i] === query[qi]) qi++;
  }
  return qi === query.length;
}
