import { useState, useRef, useEffect, useMemo, useCallback } from 'preact/hooks';
import { Plus, Search, CornerDownLeft, FolderOpen, ArrowLeft } from 'lucide-preact';
import {
  store, assignToTile, focusTile, resumeSession, createSession,
  sessionsByGroup, isSessionInTile,
} from '../state.js';
import { allTileIds, findTile } from '../tileTree.js';

// Cached capabilities from server
let _caps = null;
function getCaps() {
  if (_caps) return Promise.resolve(_caps);
  return fetch('/api/capabilities', { headers: { 'X-Moa-Request': '1' } })
    .then(r => r.json())
    .then(c => { _caps = c; return c; })
    .catch(() => ({}));
}

export function CommandPalette({ open, onClose, state, initialMode = 'search' }) {
  const [query, setQuery] = useState('');
  const [selectedIdx, setSelectedIdx] = useState(0);
  const [mode, setMode] = useState(initialMode);
  const [serverCwd, setServerCwd] = useState('');
  const [defaultModel, setDefaultModel] = useState('');
  const [creating, setCreating] = useState(false);
  const inputRef = useRef(null);
  const listRef = useRef(null);

  // Fetch capabilities on first open
  useEffect(() => {
    if (open) {
      getCaps().then(c => {
        if (c.workspaceRoot) setServerCwd(c.workspaceRoot);
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

  // Collect unique cwds from all sessions, sorted by frequency
  const recentProjects = useMemo(() => {
    const counts = {};
    for (const sess of Object.values(state.sessions)) {
      const cwd = sess.cwd || '';
      if (cwd) counts[cwd] = (counts[cwd] || 0) + 1;
    }
    return Object.entries(counts)
      .sort((a, b) => b[1] - a[1])
      .map(([cwd]) => cwd);
  }, [state.sessions]);

  // --- SEARCH MODE: session list ---
  const searchItems = useMemo(() => {
    if (mode !== 'search') return [];
    const groups = sessionsByGroup(state);
    const result = [];

    result.push({ type: 'action', id: '__new', label: 'New session…' });

    const q = query.toLowerCase().trim();
    for (const [cwd, sessions] of Object.entries(groups)) {
      const cwdLabel = cwd.split('/').pop() || cwd;
      for (const sess of sessions) {
        if (q) {
          const haystack = `${sess.title || ''} ${sess.model || ''} ${cwdLabel} ${cwd}`.toLowerCase();
          if (!fuzzyMatch(q, haystack)) continue;
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
          cwd: cwdLabel, inTile,
          saved: sess.state === 'saved',
        });
      }
    }
    return result;
  }, [state.sessions, state.tileTree, query, mode]);

  // --- CREATE MODE: project list ---
  const createItems = useMemo(() => {
    if (mode !== 'create') return [];
    const result = [];
    const q = query.toLowerCase().trim();

    // Server cwd is always the first option (default)
    if (serverCwd) {
      const label = serverCwd.split('/').pop() || serverCwd;
      if (!q || fuzzyMatch(q, serverCwd.toLowerCase())) {
        result.push({ type: 'project', cwd: serverCwd, label, isDefault: true });
      }
    }

    // Recent projects (excluding server cwd)
    for (const cwd of recentProjects) {
      if (cwd === serverCwd) continue;
      const label = cwd.split('/').pop() || cwd;
      if (q && !fuzzyMatch(q, cwd.toLowerCase()) && !fuzzyMatch(q, label.toLowerCase())) continue;
      result.push({ type: 'project', cwd, label });
    }

    // If query looks like a path and doesn't match anything, offer it as custom
    if (q && (q.startsWith('/') || q.startsWith('~'))) {
      const alreadyListed = result.some(r => r.cwd.toLowerCase() === q);
      if (!alreadyListed) {
        const label = q.split('/').pop() || q;
        result.push({ type: 'project', cwd: q, label, isCustom: true });
      }
    }

    return result;
  }, [mode, query, serverCwd, recentProjects]);

  const items = mode === 'search' ? searchItems : createItems;

  // Clamp selection
  useEffect(() => {
    if (selectedIdx >= items.length) setSelectedIdx(Math.max(0, items.length - 1));
  }, [items.length, selectedIdx]);

  // Scroll selected into view
  useEffect(() => {
    const el = listRef.current?.children[selectedIdx];
    if (el) el.scrollIntoView({ block: 'nearest' });
  }, [selectedIdx]);

  const handleSelectSearch = useCallback((item) => {
    if (item.type === 'action' && item.id === '__new') {
      setMode('create');
      return;
    }
    if (item.type === 'session') {
      if (item.saved) {
        resumeSession(item.id).catch(e => console.error('Resume failed:', e));
      } else if (item.inTile) {
        const ids = allTileIds(state.tileTree);
        for (const tid of ids) {
          const t = findTile(state.tileTree, tid);
          if (t && t.sessionId === item.id) { focusTile(tid); break; }
        }
      } else {
        assignToTile(state.focusedTile, item.id);
      }
      onClose();
    }
  }, [state.tileTree, state.focusedTile, onClose]);

  const handleSelectCreate = useCallback(async (item) => {
    if (creating) return;
    setCreating(true);
    try {
      const opts = { cwd: item.cwd };
      if (defaultModel) opts.model = defaultModel;
      await createSession(opts);
      onClose();
    } catch (e) {
      console.error('Create session failed:', e);
    } finally {
      setCreating(false);
    }
  }, [defaultModel, onClose, creating]);

  const handleSelect = useCallback((item) => {
    if (mode === 'search') return handleSelectSearch(item);
    if (mode === 'create') return handleSelectCreate(item);
  }, [mode, handleSelectSearch, handleSelectCreate]);

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
        if (mode === 'create') setMode('search');
        else onClose();
        break;
      case 'Backspace':
        if (mode === 'create' && query === '') {
          e.preventDefault();
          setMode('search');
        }
        break;
    }
  }, [items, selectedIdx, handleSelect, onClose, mode, query]);

  if (!open) return null;

  const placeholder = mode === 'search'
    ? 'Search sessions…'
    : 'Select project or type a path…';

  return (
    <div class="palette-overlay" onClick={(e) => { if (e.target === e.currentTarget) onClose(); }}>
      <div class="palette" onKeyDown={handleKeyDown}>
        <div class="palette-input-row">
          {mode === 'create' ? (
            <button class="palette-back" onClick={() => setMode('search')} title="Back">
              <ArrowLeft />
            </button>
          ) : (
            <Search class="palette-search-icon" />
          )}
          {mode === 'create' && <span class="palette-mode-label">New session</span>}
          <input
            ref={inputRef}
            class="palette-input"
            type="text"
            placeholder={placeholder}
            value={query}
            onInput={(e) => { setQuery(e.target.value); setSelectedIdx(0); }}
          />
          <kbd class="shortcut-hint palette-esc">{mode === 'create' ? 'esc: back' : 'esc'}</kbd>
        </div>
        <div class="palette-list" ref={listRef}>
          {mode === 'search' && searchItems.map((item, i) => (
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
                <span class={`state-dot ${item.state}`} />
                <span class="palette-item-label">{item.title}</span>
                <span class="palette-item-meta">{item.model}</span>
                <span class="palette-item-cwd">{item.cwd}</span>
                {item.inTile && <span class="palette-tile-badge">{item.inTile}</span>}
                {i === selectedIdx && <CornerDownLeft class="palette-enter-hint" />}
              </div>
            )
          ))}
          {mode === 'create' && createItems.map((item, i) => (
            <div
              key={item.cwd}
              class={`palette-item palette-project ${i === selectedIdx ? 'selected' : ''} ${creating ? 'creating' : ''}`}
              onClick={() => handleSelect(item)}
              onMouseEnter={() => setSelectedIdx(i)}
            >
              <FolderOpen class="palette-item-icon" />
              <span class="palette-item-label">
                {item.label}
                {item.isDefault && <span class="palette-default-tag">server cwd</span>}
                {item.isCustom && <span class="palette-custom-tag">custom path</span>}
              </span>
              <span class="palette-item-cwd palette-item-cwd-full">{item.cwd}</span>
              {i === selectedIdx && <CornerDownLeft class="palette-enter-hint" />}
            </div>
          ))}
          {items.length === 0 && (
            <div class="palette-empty">
              {mode === 'create' ? 'Type a path to create a session there' : 'No matching sessions'}
            </div>
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
