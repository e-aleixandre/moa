import { useState, useEffect, useMemo, useRef, useCallback } from 'preact/hooks';
import { Search, X, FolderOpen, Folder, ChevronRight, Check, CornerDownLeft } from 'lucide-preact';
import { createSession } from '../session-actions.js';

// tildify/expandHome mirror the server's ~ handling so displayed paths are
// short and typed ~ paths resolve. home is the server user's home dir from
// /api/capabilities; when unknown, paths are shown verbatim.
function tildify(path, home) {
  if (!home) return path;
  if (path === home) return '~';
  if (path.startsWith(home + '/')) return '~' + path.slice(home.length);
  return path;
}
function expandHome(path, home) {
  if (!home) return path;
  if (path === '~') return home;
  if (path.startsWith('~/')) return home + path.slice(1);
  return path;
}
function basename(p) {
  const parts = p.split('/').filter(Boolean);
  return parts.pop() || '/';
}
function parentDir(p) {
  const parts = p.split('/').filter(Boolean);
  parts.pop();
  return '/' + parts.join('/');
}

// truncMiddle shortens a path for display, always preserving the tail (the end
// segments are what distinguish .../moa/main from .../moa/usage-openai). Keeps
// the head (~ or first one/two segments) and elides the middle with "…".
function truncMiddle(path, home, max = 40) {
  const s = tildify(path, home);
  if (s.length <= max) return s;
  const parts = s.split('/');
  let head = parts[0];
  if (parts.length > 3 && (parts[0] + '/' + parts[1]).length + 4 < max / 2) {
    head = parts[0] + '/' + parts[1];
  }
  const headSegs = head.split('/').length;
  let tailStart = parts.length - 1;
  while (tailStart - 1 > headSegs - 1) {
    const cand = parts.slice(tailStart - 1).join('/');
    if ((head + '/…/' + cand).length <= max) tailStart--; else break;
  }
  let tail = parts.slice(tailStart).join('/');
  let out = head + '/…/' + tail;
  if (out.length > max) {
    tail = '…' + tail.slice(tail.length - Math.max(0, max - head.length - 4));
    out = head + '/' + tail;
  }
  return out;
}

function relativeWhen(ms) {
  if (!ms) return '';
  const diff = Date.now() - ms;
  const m = Math.floor(diff / 60000);
  if (m < 1) return 'now';
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h`;
  const d = Math.floor(h / 24);
  if (d < 7) return `${d}d`;
  return `${Math.floor(d / 7)}w`;
}

// NewSessionSheet is the create-a-session flow: pick a recent project or browse
// the filesystem for a working directory. Shares one shell (header, search
// input, scroll body, fixed confirm bar) between two views — Recents and
// Explorer — so it reads the same on desktop (framed) and mobile (full-screen).
export function NewSessionSheet({ state, serverCwd, homeDir, defaultModel, onClose, onBack }) {
  const [query, setQuery] = useState('');
  const [view, setView] = useState('recents'); // 'recents' | 'explore'
  const [selectedRecent, setSelectedRecent] = useState('');
  const [exploreDir, setExploreDir] = useState(serverCwd || '');
  const [entries, setEntries] = useState([]);   // subdir names of exploreDir
  const [dirFilter, setDirFilter] = useState(''); // filter typed after a known dir
  const [loadingDir, setLoadingDir] = useState(false);
  const [creating, setCreating] = useState(false);
  const inputRef = useRef(null);
  const bodyRef = useRef(null);

  useEffect(() => {
    requestAnimationFrame(() => inputRef.current?.focus());
  }, []);

  // Recent projects: unique cwds across sessions, each with its most recent
  // activity and a duplicate-basename flag so "main" folders disambiguate by
  // their parent (moa / main vs winerim-backend / main).
  const recents = useMemo(() => {
    const byCwd = {};
    for (const sess of Object.values(state.sessions)) {
      const cwd = sess.cwd || '';
      if (!cwd) continue;
      const updated = sess.updated || 0;
      if (!byCwd[cwd] || updated > byCwd[cwd].updated) {
        byCwd[cwd] = { cwd, updated };
      }
    }
    // The server cwd is always offered first, even with no session there yet.
    if (serverCwd && !byCwd[serverCwd]) {
      byCwd[serverCwd] = { cwd: serverCwd, updated: 0, isDefault: true };
    } else if (serverCwd && byCwd[serverCwd]) {
      byCwd[serverCwd].isDefault = true;
    }
    const list = Object.values(byCwd).sort((a, b) => {
      if (a.isDefault) return -1;
      if (b.isDefault) return 1;
      return b.updated - a.updated;
    });
    // Duplicate-basename detection for disambiguation.
    const baseCounts = {};
    for (const r of list) baseCounts[basename(r.cwd)] = (baseCounts[basename(r.cwd)] || 0) + 1;
    for (const r of list) {
      const base = basename(r.cwd);
      r.name = base;
      r.ctx = baseCounts[base] > 1 ? basename(parentDir(r.cwd)) + ' / ' : '';
    }
    return list;
  }, [state.sessions, serverCwd]);

  const filteredRecents = useMemo(() => {
    const f = query.toLowerCase().trim();
    if (!f) return recents;
    return recents.filter(r =>
      basename(r.cwd).toLowerCase().includes(f) ||
      tildify(r.cwd, homeDir).toLowerCase().includes(f));
  }, [recents, query, homeDir]);

  // Fetch the subdirectories of exploreDir from the server, debounced. Cancelled
  // on cleanup so a stale response can't clobber a newer directory's listing.
  useEffect(() => {
    if (view !== 'explore' || !exploreDir) return;
    let cancelled = false;
    setLoadingDir(true);
    const timer = setTimeout(() => {
      // Trailing slash => "list everything in this dir" (see handleFSComplete).
      fetch('/api/fs/complete?path=' + encodeURIComponent(exploreDir + '/'), { headers: { 'X-Moa-Request': '1' } })
        .then(r => r.json())
        .then(data => {
          if (cancelled) return;
          setEntries(Array.isArray(data.entries) ? data.entries : []);
          setLoadingDir(false);
        })
        .catch(() => { if (!cancelled) { setEntries([]); setLoadingDir(false); } });
    }, 120);
    return () => { cancelled = true; clearTimeout(timer); };
  }, [view, exploreDir]);

  const shownDirs = useMemo(() => {
    const f = dirFilter.toLowerCase();
    return entries.filter(name => !f || name.toLowerCase().startsWith(f));
  }, [entries, dirFilter]);

  // Breadcrumb segments for the current explore dir, each with its full path.
  const crumbs = useMemo(() => {
    const disp = tildify(exploreDir, homeDir);
    const segs = disp.split('/').filter(s => s !== '');
    const out = [];
    let acc = disp.startsWith('~') ? homeDir : '';
    segs.forEach((s, i) => {
      if (i === 0 && s === '~') { acc = homeDir; out.push({ label: '~', path: homeDir }); }
      else { acc = (out[i - 1]?.path || '') + '/' + s; out.push({ label: s, path: acc }); }
    });
    return out;
  }, [exploreDir, homeDir]);

  const goToDir = useCallback((path) => {
    setExploreDir(path);
    setDirFilter('');
    setView('explore');
    setQuery(tildify(path, homeDir));
    if (bodyRef.current) bodyRef.current.scrollTop = 0;
  }, [homeDir]);

  const backToRecents = useCallback(() => {
    setView('recents');
    setQuery('');
    setDirFilter('');
  }, []);

  // Input drives both views: plain text filters recents; a path (/ or ~) feeds
  // the explorer, resolving the deepest existing prefix and using the rest as a
  // subdir filter.
  const onInput = useCallback((e) => {
    const v = e.target.value;
    setQuery(v);
    const isPath = v.startsWith('/') || v.startsWith('~');
    if (!isPath) { setView('recents'); return; }
    const expanded = expandHome(v.replace(/\/+$/, ''), homeDir) || '/';
    setView('explore');
    // If the exact path is a dir we can just list it; otherwise treat the last
    // segment as a filter over the parent's children. The server validates on
    // create, so an optimistic split here is safe.
    if (v.endsWith('/')) {
      setExploreDir(expanded);
      setDirFilter('');
    } else {
      const cut = expanded.lastIndexOf('/');
      const parent = cut <= 0 ? '/' : expanded.slice(0, cut);
      setExploreDir(parent);
      setDirFilter(expanded.slice(cut + 1));
    }
  }, [homeDir]);

  const clearInput = useCallback(() => {
    setQuery('');
    backToRecents();
    requestAnimationFrame(() => inputRef.current?.focus());
  }, [backToRecents]);

  const target = view === 'recents' ? selectedRecent : exploreDir;

  const doCreate = useCallback(async () => {
    if (!target || creating) return;
    setCreating(true);
    try {
      const opts = { cwd: target };
      if (defaultModel) opts.model = defaultModel;
      await createSession(opts);
      onClose();
    } catch (err) {
      console.error('Create session failed:', err);
      setCreating(false);
    }
  }, [target, creating, defaultModel, onClose]);

  const onKeyDown = useCallback((e) => {
    if (e.key === 'Escape') {
      e.preventDefault();
      if (view === 'explore') backToRecents();
      else if (onBack) onBack();
      else onClose();
    } else if (e.key === 'Enter' && target) {
      e.preventDefault();
      doCreate();
    }
  }, [view, target, backToRecents, onBack, onClose, doCreate]);

  return (
    <div class="newsession" onKeyDown={onKeyDown}>
      <div class="ns-header">
        <span class="ns-title">New session</span>
        <button class="ns-close" onClick={onBack || onClose} title="Close"><X /></button>
      </div>

      <div class="ns-searchwrap">
        <div class="ns-searchbox">
          <Search class="ns-search-icon" />
          <input
            ref={inputRef}
            class={`ns-input ${query.startsWith('/') || query.startsWith('~') ? 'is-path' : ''}`}
            type="text"
            autocomplete="off"
            autocapitalize="off"
            spellcheck={false}
            placeholder="Search a project or type a path…"
            value={query}
            onInput={onInput}
          />
          {query && <button class="ns-clear" onClick={clearInput} title="Clear"><X /></button>}
        </div>
      </div>

      <div class="ns-body" ref={bodyRef}>
        {view === 'recents' ? (
          <>
            <div class="ns-section-label">Recent projects</div>
            {filteredRecents.length === 0 ? (
              <div class="ns-empty">
                <strong>No matches</strong>
                Try another name, or type a path (/ or ~) to browse
              </div>
            ) : filteredRecents.map(r => (
              <button
                key={r.cwd}
                class={`ns-recent ${selectedRecent === r.cwd ? 'selected' : ''}`}
                onClick={() => setSelectedRecent(r.cwd)}
                onDblClick={doCreate}
              >
                <span class="ns-recent-icon"><FolderOpen /></span>
                <span class="ns-recent-meta">
                  <span class="ns-recent-name">
                    {r.ctx && <span class="ns-recent-ctx">{r.ctx}</span>}{r.name}
                    {r.isDefault && <span class="ns-recent-default">server cwd</span>}
                  </span>
                  <span class="ns-recent-path">{truncMiddle(r.cwd, homeDir)}</span>
                </span>
                {selectedRecent === r.cwd
                  ? <Check class="ns-recent-check" />
                  : <span class="ns-recent-when">{relativeWhen(r.updated)}</span>}
              </button>
            ))}
            <button class="ns-explore-entry" onClick={() => goToDir(serverCwd || homeDir || '/')}>
              <Folder />
              <span>Browse folders</span>
              <ChevronRight class="ns-explore-chev" />
            </button>
          </>
        ) : (
          <>
            <button class="ns-back-recents" onClick={backToRecents}>‹&nbsp; Recents</button>
            <nav class="ns-crumbs">
              {crumbs.map((c, i) => {
                const last = i === crumbs.length - 1;
                return (
                  <>
                    <button
                      key={c.path}
                      class={`ns-crumb ${last ? 'current' : ''}`}
                      onClick={() => !last && goToDir(c.path)}
                      disabled={last}
                    >{c.label}</button>
                    {!last && <span class="ns-crumb-sep">/</span>}
                  </>
                );
              })}
            </nav>
            {loadingDir && entries.length === 0 ? (
              <div class="ns-empty"><strong>Loading…</strong></div>
            ) : shownDirs.length === 0 ? (
              <div class="ns-empty">
                <strong>{dirFilter ? `No folder starts with “${dirFilter}”` : 'No subfolders'}</strong>
                You can create the session right here
              </div>
            ) : (
              <div class="ns-dirlist">
                {shownDirs.map(name => {
                  const full = exploreDir === '/' ? '/' + name : exploreDir + '/' + name;
                  const isRecent = recents.some(r => r.cwd === full);
                  return (
                    <button
                      key={full}
                      class={`ns-dir ${name.startsWith('.') ? 'hidden-dir' : ''}`}
                      onClick={() => goToDir(full)}
                    >
                      <Folder />
                      <span class="ns-dir-name">{name}</span>
                      {isRecent && <span class="ns-dir-badge">recent</span>}
                      <ChevronRight class="ns-dir-chev" />
                    </button>
                  );
                })}
              </div>
            )}
          </>
        )}
      </div>

      <div class="ns-confirmbar">
        <div class="ns-target">
          {target ? (
            <><Folder class="ns-target-icon" /><span class="ns-target-path">{truncMiddle(target, homeDir, 46)}</span></>
          ) : (
            <span class="ns-target-none">Pick a project or a folder</span>
          )}
        </div>
        <button class="ns-create" onClick={doCreate} disabled={!target || creating}>
          {creating ? 'Creating…' : 'Create session here'}
          {target && !creating && <CornerDownLeft class="ns-create-hint" />}
        </button>
      </div>
    </div>
  );
}
