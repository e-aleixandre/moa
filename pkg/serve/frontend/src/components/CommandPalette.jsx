import { useState, useRef, useEffect, useMemo, useCallback } from 'preact/hooks';
import { Plus, Search, CornerDownLeft, FolderOpen, ArrowLeft, ChevronRight } from 'lucide-preact';
import { store, sessionsByGroup, isSessionInTile } from '../store.js';
import { assignToTile } from '../tile-actions.js';
import { resumeSession, createSession } from '../session-actions.js';
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
  const [fsInfo, setFsInfo] = useState(null);
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

  // Fetch fs completion info (existence + subdirs) for path-like create-mode
  // queries, debounced. Cancelled on cleanup so stale responses can't clobber
  // a newer query's result.
  useEffect(() => {
    // Use the raw (non-lowercased) text: filesystem paths are case-sensitive
    // on the server, so validation must match exactly what will be created.
    const path = query.trim();
    if (mode !== 'create' || !path || !(path.startsWith('/') || path.startsWith('~'))) {
      setFsInfo(null);
      return;
    }
    let cancelled = false;
    const timer = setTimeout(() => {
      fetch('/api/fs/complete?path=' + encodeURIComponent(path), { headers: { 'X-Moa-Request': '1' } })
        .then(r => r.json())
        .then(data => { if (!cancelled) setFsInfo(data); })
        .catch(() => { if (!cancelled) setFsInfo(null); });
    }, 150);
    return () => { cancelled = true; clearTimeout(timer); };
  }, [query, mode]);

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

    // If query looks like a path, offer it as a validated create item, plus
    // drill-down items for its matching subdirectories. Use the raw text (not
    // lowercased q): paths are case-sensitive on the server.
    const path = query.trim();
    if (path && (path.startsWith('/') || path.startsWith('~'))) {
      const alreadyListed = result.some(r => r.cwd.toLowerCase() === path.toLowerCase());
      if (!alreadyListed) {
        const label = path.split('/').pop() || path;
        const valid = fsInfo ? !!fsInfo.isDir : undefined;
        result.push({ type: 'project', cwd: path, label, isCustom: true, valid });
      }

      if (fsInfo && fsInfo.entries) {
        const dir = dirOf(path);
        for (const name of fsInfo.entries) {
          result.push({ type: 'project', cwd: joinPath(dir, name), label: name, drill: true });
        }
      }
    }

    return result;
  }, [mode, query, serverCwd, recentProjects, fsInfo]);

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
        // Resume puts the session into the focused tile (see state.resumeSession).
        resumeSession(item.id).catch(e => console.error('Resume failed:', e));
      } else {
        // Always assign to the focused tile. If the session was visible
        // elsewhere, assignToTile clears it from the old tile first.
        assignToTile(state.focusedTile, item.id);
      }
      onClose();
    }
  }, [state.focusedTile, onClose]);

  const handleSelectCreate = useCallback(async (item) => {
    if (creating) return;
    if (item.drill) {
      // Drill down into the subdirectory instead of creating a session.
      setQuery(item.cwd + '/');
      setSelectedIdx(0);
      return;
    }
    if (item.isCustom && item.valid === false) return; // known-invalid path — the ✗ explains why; unknown (loading/error) still tries, server validates
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
              class={`palette-item palette-project ${item.drill ? 'palette-dir' : ''} ${i === selectedIdx ? 'selected' : ''} ${creating ? 'creating' : ''}`}
              onClick={() => handleSelect(item)}
              onMouseEnter={() => setSelectedIdx(i)}
            >
              <FolderOpen class="palette-item-icon" />
              <span class="palette-item-label">
                {item.label}
                {item.isDefault && <span class="palette-default-tag">server cwd</span>}
                {item.isCustom && <span class="palette-custom-tag">custom path</span>}
                {item.isCustom && item.valid === true && <span class="palette-valid-badge palette-valid-yes">✓</span>}
                {item.isCustom && item.valid === false && <span class="palette-valid-badge palette-valid-no">✗</span>}
              </span>
              <span class="palette-item-cwd palette-item-cwd-full">{item.cwd}</span>
              {item.drill
                ? <ChevronRight class="palette-item-icon" />
                : (i === selectedIdx && <CornerDownLeft class="palette-enter-hint" />)}
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

// dirOf returns the directory prefix of a typed path (including the trailing
// slash), for building drill-down child paths. No filesystem normalization —
// the server canonicalizes.
function dirOf(path) {
  if (path.endsWith('/')) return path;
  const idx = path.lastIndexOf('/');
  return idx >= 0 ? path.slice(0, idx + 1) : '';
}

function joinPath(dir, name) {
  return dir + name;
}
