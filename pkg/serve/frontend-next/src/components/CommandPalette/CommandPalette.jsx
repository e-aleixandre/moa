import { useState, useEffect, useMemo, useRef, useCallback } from "preact/hooks";
import {
  Search, Plus, LayoutGrid, MessageSquare, CornerDownLeft,
  ArrowLeft, Folder, FolderOpen, ChevronRight, Smartphone,
} from "lucide-preact";
import { store } from "../../data/store.js";
import { closePalette } from "../../data/palette.js";
import { openPulsePairing } from "../../data/pulse-pairing-panel.js";
import { fuzzyMatch, fuzzyMatchIndices } from "../../data/fuzzy.js";
import {
  createSession, resumeSession, unarchiveSession,
} from "../../data/session-actions.js";
import { assignToTile, openSession } from "../../data/tile-actions.js";
import { navigate } from "../../data/router.js";
import { allTileIds, findTile } from "../../data/tileTree.js";
import { addToast } from "../../data/notifications.js";
import {
  sessionTitle, sessionDotState, isRecentSession, projectLabel,
} from "../../data/util/format.js";
import { modLabel } from "../../data/util/shortcut.js";
import "./CommandPalette.css";

// ── Cached capabilities (workspaceRoot / homeDir / defaultModel). Module-level
// so it's fetched once across every palette open (getCaps pattern from the old
// SPA + Composer.jsx). Models are cached lazily on first entry to create.
let _caps = null;
function getCaps() {
  if (_caps) return Promise.resolve(_caps);
  return fetch("/api/capabilities", { headers: { "X-Moa-Request": "1" } })
    .then((r) => r.json())
    .then((c) => { _caps = c; return c; })
    .catch(() => ({}));
}
let _models = null;
function getModels() {
  if (_models) return Promise.resolve(_models);
  return fetch("/api/models", { headers: { "X-Moa-Request": "1" } })
    .then((r) => r.json())
    .then((m) => { _models = Array.isArray(m) ? m : []; return _models; })
    .catch(() => []);
}

// ── Pure path helpers — ported verbatim from the old SPA's NewSessionSheet
// (tildify/expandHome/basename/parentDir/truncMiddle/relativeWhen). They mirror
// the server's ~ handling so displayed paths stay short and typed ~ paths
// resolve.
function tildify(path, home) {
  if (!home) return path;
  if (path === home) return "~";
  if (path.startsWith(home + "/")) return "~" + path.slice(home.length);
  return path;
}
function expandHome(path, home) {
  if (!home) return path;
  if (path === "~") return home;
  if (path.startsWith("~/")) return home + path.slice(1);
  return path;
}
function basename(p) {
  const parts = p.split("/").filter(Boolean);
  return parts.pop() || "/";
}
function parentDir(p) {
  const parts = p.split("/").filter(Boolean);
  parts.pop();
  return "/" + parts.join("/");
}
function truncMiddle(path, home, max = 40) {
  const s = tildify(path, home);
  if (s.length <= max) return s;
  const parts = s.split("/");
  let head = parts[0];
  if (parts.length > 3 && (parts[0] + "/" + parts[1]).length + 4 < max / 2) {
    head = parts[0] + "/" + parts[1];
  }
  const headSegs = head.split("/").length;
  let tailStart = parts.length - 1;
  while (tailStart - 1 > headSegs - 1) {
    const cand = parts.slice(tailStart - 1).join("/");
    if ((head + "/…/" + cand).length <= max) tailStart--; else break;
  }
  let tail = parts.slice(tailStart).join("/");
  let out = head + "/…/" + tail;
  if (out.length > max) {
    tail = "…" + tail.slice(tail.length - Math.max(0, max - head.length - 4));
    out = head + "/" + tail;
  }
  return out;
}
function relativeWhen(ms) {
  if (!ms) return "";
  const diff = Date.now() - ms;
  const m = Math.floor(diff / 60000);
  if (m < 1) return "now";
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h`;
  const d = Math.floor(h / 24);
  if (d < 7) return `${d}d`;
  return `${Math.floor(d / 7)}w`;
}

// modelSpec builds the "provider/id" spec createSession/configureSession send
// over the wire (matches ConversationScreen.deriveModelSpecs + FullModelSpec).
function modelSpec(m) {
  return m.provider ? `${m.provider}/${m.id}` : m.id;
}

// liveStatus derives the one-line status column (spec §7). It reads ONLY fields
// the poll / WS already put on state.sessions — no invented fields. Rich text
// degrades to the simple state when the richer datum is absent. Returns
// { text, tone } where tone 'y' paints it yellow (needs-you).
function liveStatus(sess) {
  const st = sess.state || "idle";
  if (st === "permission" || sess.pendingPerm) {
    const tool = sess.pendingPerm?.tool_name;
    return { text: tool ? `needs you — ${tool}` : "needs you", tone: "y" };
  }
  if (st === "error") {
    return { text: sess.error ? String(sess.error) : "error", tone: "" };
  }
  if (st === "saved") {
    return { text: "saved", tone: "" };
  }
  // running / idle: prefer the server's cheap brief when present, else a plain
  // state word (no gerund invention here — the brief is the rich datum).
  const brief = (sess.briefProgress || sess.briefAttempting || "").trim();
  if (brief) return { text: brief, tone: "" };
  if (st === "running") return { text: "running…", tone: "" };
  return { text: "idle", tone: "" };
}

// paneOf maps a sessionId → its 1-based pane index (for the P*n* badge), or
// null when the session isn't currently in a tile.
function paneOf(tree, sessionId) {
  const ids = allTileIds(tree);
  for (let i = 0; i < ids.length; i++) {
    const t = findTile(tree, ids[i]);
    if (t && t.sessionId === sessionId) return i + 1;
  }
  return null;
}

// Highlight — wraps fuzzy-matched characters of `text` (against lowercased
// query) in <span class="hl">. When the match came from another field
// (cwd/model) there are no title indices, so it renders plain.
function Highlight({ text, query }) {
  if (!query) return <>{text}</>;
  const idx = fuzzyMatchIndices(query, text.toLowerCase());
  if (!idx || idx.length === 0) return <>{text}</>;
  const set = new Set(idx);
  const out = [];
  let run = "";
  let hl = false;
  for (let i = 0; i < text.length; i++) {
    const on = set.has(i);
    if (on !== hl && run) {
      out.push(hl ? <span class="hl" key={i}>{run}</span> : run);
      run = "";
    }
    hl = on;
    run += text[i];
  }
  if (run) out.push(hl ? <span class="hl" key="last">{run}</span> : run);
  return <>{out}</>;
}

const CAP_NO_QUERY = 8; // spec §2 — cap the no-query session list; scroll for more

// CommandPalette — the ⌘K palette (5H). One ranked list of sessions + actions
// (no modes), plus a create-session step. Mounted ONCE globally in app.jsx,
// outside the view switch, so it's the same organism over conversation / grid /
// mobile. It subscribes to the store itself for the live session list (never
// takes sessions by prop) so per-poll changes reflect without a parent re-render.
export function CommandPalette({
  open,
  onClose,
  context = "conversation",
  focusedPane = null,
  initialStep = "search",
}) {
  const [state, setState] = useState(store.get());
  useEffect(() => store.subscribe(setState), []);

  const [step, setStep] = useState(initialStep);
  const [query, setQuery] = useState("");
  const [selectedIdx, setSelectedIdx] = useState(0);

  // create-step state
  const [caps, setCaps] = useState(_caps || {});
  const [models, setModels] = useState(_models || []);
  const [model, setModel] = useState("");
  const [exploreDir, setExploreDir] = useState("");
  const [dirFilter, setDirFilter] = useState("");
  const [browseEntries, setBrowseEntries] = useState([]);
  const [loadingDir, setLoadingDir] = useState(false);
  const [browseErr, setBrowseErr] = useState(false);
  const [creating, setCreating] = useState(false);

  const inputRef = useRef(null);
  const listRef = useRef(null);
  const openerRef = useRef(null);
  // Synchronous in-flight guard against double-activation: reactive state
  // (`creating`) doesn't settle between two fast Enter presses, so a double
  // Enter would fire createSession/resumeSession/assignToTile twice.
  const inFlightRef = useRef(false);
  const homeDir = caps.homeDir || "";
  const serverCwd = caps.workspaceRoot || "";
  const isMobile = context === "mobile";

  // On open: remember the opener (to restore focus on close), reset transient
  // state, fetch caps, and focus the input next frame (spec §6/§9).
  useEffect(() => {
    if (!open) return;
    openerRef.current = document.activeElement;
    setStep(initialStep);
    setQuery("");
    setSelectedIdx(0);
    setCreating(false);
    inFlightRef.current = false;
    setBrowseErr(false);
    getCaps().then((c) => {
      setCaps(c);
      if (c.defaultModel && !model) setModel(c.defaultModel);
    });
    requestAnimationFrame(() => inputRef.current?.focus());
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, initialStep]);

  // Restore focus to the opener when the palette closes.
  useEffect(() => {
    if (open) return;
    const el = openerRef.current;
    if (el && typeof el.focus === "function") el.focus();
  }, [open]);

  // Entering the create step: lazy-load models, seed the browse root + default
  // model, refocus the (now cleared) input.
  useEffect(() => {
    if (!open || step !== "create") return;
    setQuery("");
    setSelectedIdx(0);
    setExploreDir(serverCwd || homeDir || "/");
    setDirFilter("");
    getModels().then((m) => {
      setModels(m);
      if (!model) {
        const def = caps.defaultModel || (m[0] ? modelSpec(m[0]) : "");
        if (def) setModel(def);
      }
    });
    requestAnimationFrame(() => inputRef.current?.focus());
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, step]);

  // ── SEARCH: recent-projects for the create step (dedupe basename) ───────────
  const recents = useMemo(() => {
    const byCwd = {};
    for (const sess of Object.values(state.sessions)) {
      const cwd = sess.cwd || "";
      if (!cwd) continue;
      const updated = sess.updated || 0;
      if (!byCwd[cwd] || updated > byCwd[cwd].updated) byCwd[cwd] = { cwd, updated };
    }
    if (serverCwd && !byCwd[serverCwd]) byCwd[serverCwd] = { cwd: serverCwd, updated: 0, isDefault: true };
    else if (serverCwd && byCwd[serverCwd]) byCwd[serverCwd].isDefault = true;
    const list = Object.values(byCwd).sort((a, b) => {
      if (a.isDefault) return -1;
      if (b.isDefault) return 1;
      return b.updated - a.updated;
    });
    const baseCounts = {};
    for (const r of list) baseCounts[basename(r.cwd)] = (baseCounts[basename(r.cwd)] || 0) + 1;
    for (const r of list) {
      const base = basename(r.cwd);
      r.name = base;
      r.ctx = baseCounts[base] > 1 ? basename(parentDir(r.cwd)) + " / " : "";
    }
    return list;
  }, [state.sessions, serverCwd]);

  // Debounced /api/fs/complete for the create-step explorer (ported from the
  // old NewSessionSheet: trailing slash = "list this dir", cancelled on cleanup
  // so a stale response can't clobber a newer directory).
  useEffect(() => {
    if (!open || step !== "create" || !exploreDir) return;
    let cancelled = false;
    setLoadingDir(true);
    setBrowseErr(false);
    const timer = setTimeout(() => {
      fetch("/api/fs/complete?path=" + encodeURIComponent(exploreDir + "/"), { headers: { "X-Moa-Request": "1" } })
        .then((r) => r.json())
        .then((data) => {
          if (cancelled) return;
          setBrowseEntries(Array.isArray(data.entries) ? data.entries : []);
          setLoadingDir(false);
        })
        .catch(() => { if (!cancelled) { setBrowseEntries([]); setLoadingDir(false); setBrowseErr(true); } });
    }, 130);
    return () => { cancelled = true; clearTimeout(timer); };
  }, [open, step, exploreDir]);

  // ── Actions catalogue (context-aware). CORE set only; NICE-TO-HAVE presets/
  // archive left as TODO (spec §3).
  const actions = useMemo(() => {
    const list = [];
    list.push({
      id: "__new", label: "New session…", sublabel: "pick project & model",
      icon: <Plus size={14} />, accent: "", shortcut: [modLabel, "N"],
      run: () => setStep("create"),
    });
    if (context === "grid") {
      list.push({
        id: "__conversation", label: "Go to conversation", sublabel: "single-session view",
        icon: <MessageSquare size={14} />, accent: "blue", shortcut: [modLabel, "G"],
        run: () => { onClose(); navigate(null); },
      });
    } else if (context === "conversation") {
      list.push({
        id: "__grid", label: "Go to grid", sublabel: "multi-session view",
        icon: <LayoutGrid size={14} />, accent: "blue", shortcut: [modLabel, "G"],
        run: () => { onClose(); navigate("grid"); },
      });
    }
    // Pair Pulse — opens the QR pairing panel (5N). Available in every context;
    // pairing is a device-wide action, not session- or view-scoped.
    list.push({
      id: "__pair-pulse", label: "Pair Pulse…", sublabel: "connect a phone via QR",
      icon: <Smartphone size={14} />, accent: "", shortcut: null,
      run: () => { onClose(); openPulsePairing(); },
    });
    // TODO 5H nice-to-have: Layout preset actions (grid only), Archive current,
    // Settings — cheap via this action.run() pattern, left out to keep CORE tight.
    return list;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [context, onClose]);

  // ── Build the flat item list for the SEARCH step ───────────────────────────
  const searchItems = useMemo(() => {
    const q = query.toLowerCase().trim();
    const out = [];

    // Sessions (MRU). No query → recent, non-archived, capped. Query → fuzzy
    // over every session (incl. archived), no cap.
    const all = Object.values(state.sessions).sort((a, b) => (b.updated || 0) - (a.updated || 0));
    const sessRows = [];
    for (const sess of all) {
      const cwd = sess.cwd || "";
      const cwdLabel = projectLabel(cwd);
      const title = sessionTitle(sess);
      if (q) {
        const hay = `${title} ${sess.model || ""} ${cwdLabel} ${cwd}`.toLowerCase();
        if (!fuzzyMatch(q, hay)) continue;
      } else {
        if (sess.archived) continue;
        if (!isRecentSession(sess)) continue;
      }
      sessRows.push({
        kind: "session",
        id: sess.id,
        title,
        dotState: sessionDotState(sess),
        live: liveStatus(sess),
        cwdLabel,
        when: relativeWhen(sess.updated),
        paneN: paneOf(state.tileTree, sess.id),
        archived: !!sess.archived,
        saved: sess.state === "saved",
      });
    }
    const cappedSessions = q ? sessRows : sessRows.slice(0, CAP_NO_QUERY);
    if (cappedSessions.length) {
      out.push({ kind: "group", label: q ? "Sessions" : "Sessions · recent" });
      out.push(...cappedSessions);
    }

    // Actions (fuzzy over label when there's a query; all when empty).
    const actRows = actions.filter((a) => !q || fuzzyMatch(q, a.label.toLowerCase()));
    if (actRows.length) {
      out.push({ kind: "group", label: "Actions" });
      for (const a of actRows) out.push({ kind: "action", ...a });
    }

    // Empty state (spec §7): zero session AND zero action hits → one selectable
    // "create from query" row (CORE fallback = open create with the query).
    const hasHits = cappedSessions.length > 0 || actRows.length > 0;
    if (q && !hasHits) {
      out.push({ kind: "create-from-query", query });
    }
    return out;
  }, [state.sessions, state.tileTree, query, actions]);

  // ── Build the flat item list for the CREATE step ───────────────────────────
  const createItems = useMemo(() => {
    const out = [];
    const raw = query;
    const isPath = raw.startsWith("/") || raw.startsWith("~");
    if (!isPath) {
      // Recents view: filter recent projects by basename / tildified path.
      const f = raw.toLowerCase().trim();
      const filtered = recents.filter(
        (r) => !f || basename(r.cwd).toLowerCase().includes(f) || tildify(r.cwd, homeDir).toLowerCase().includes(f),
      );
      if (filtered.length) {
        out.push({ kind: "group", label: "Recent projects" });
        for (const r of filtered) {
          out.push({
            kind: "project",
            cwd: r.cwd,
            display: truncMiddle(r.cwd, homeDir, 44),
            badge: r.isDefault ? "default" : (r.updated ? "recent" : null),
            when: relativeWhen(r.updated),
          });
        }
      }
    }
    // Browse view (path typed, or always show the current dir's children so the
    // explorer is reachable). Shown whenever there are entries or it's loading.
    const f = dirFilter.toLowerCase();
    const shown = browseEntries.filter((n) => !f || n.toLowerCase().startsWith(f));
    if (isPath || shown.length || loadingDir || browseErr) {
      out.push({ kind: "group", label: `Browse · ${tildify(exploreDir, homeDir)}` });
      if (browseErr) {
        out.push({ kind: "note", text: "Could not read this folder" , error: true });
      } else if (loadingDir && browseEntries.length === 0) {
        out.push({ kind: "note", text: "Loading…" });
      } else if (shown.length === 0) {
        out.push({ kind: "note", text: dirFilter ? `No folder starts with “${dirFilter}”` : "No subfolders — ⏎ creates here" });
      } else {
        for (const name of shown) {
          const full = exploreDir === "/" ? "/" + name : exploreDir + "/" + name;
          out.push({ kind: "dir", name, path: full });
        }
      }
    }
    return out;
  }, [query, recents, homeDir, dirFilter, browseEntries, loadingDir, browseErr, exploreDir]);

  const items = step === "create" ? createItems : searchItems;

  // Selectable indices (skip groups / notes). selectedIdx indexes into this.
  const selectable = useMemo(
    () => items.filter((it) => it.kind !== "group" && it.kind !== "note"),
    [items],
  );

  // Clamp selection when the list shrinks.
  useEffect(() => {
    if (selectedIdx >= selectable.length) setSelectedIdx(Math.max(0, selectable.length - 1));
  }, [selectable.length, selectedIdx]);

  // Scroll the selected row into view.
  useEffect(() => {
    const el = listRef.current?.querySelector(`[data-sel="${selectedIdx}"]`);
    if (el) el.scrollIntoView({ block: "nearest" });
  }, [selectedIdx, items]);

  // createTarget — the cwd the create bar will use: the selected project row's
  // cwd, else the navigated explore dir (spec §5).
  const createTarget = useMemo(() => {
    if (step !== "create") return "";
    const sel = selectable[selectedIdx];
    if (sel && sel.kind === "project") return sel.cwd;
    if (query.startsWith("/") || query.startsWith("~")) return exploreDir;
    if (sel && sel.kind === "dir") return exploreDir; // hovering a dir → parent as fallback
    return exploreDir || serverCwd;
  }, [step, selectable, selectedIdx, query, exploreDir, serverCwd]);

  // ── Session activation (verbs by context, spec §4) ──────────────────────────
  const activateSession = useCallback(async (item, secondary) => {
    if (inFlightRef.current) return;
    inFlightRef.current = true;
    const id = item.id;
    if (item.archived) {
      try { await unarchiveSession(id); } catch (e) { console.error("Unarchive failed:", e); }
    }
    // Saved / archived sessions auto-resume once visible (afterVisibilityChange,
    // already ported) — assigning/opening is enough; resumeSession is only used
    // for the explicit conversation-open path so the reader gets immediate focus.
    try {
      if (context === "grid") {
        if (secondary) { openSession(id); onClose(); inFlightRef.current = false; navigate(null); return; }
        // focusedPane is a 1-based DFS index (for copy/footer); resolve it to the
        // real tileId — after presets/splits the ids don't line up with 1..N.
        const ids = allTileIds(store.get().tileTree);
        const tile = (focusedPane != null && ids[focusedPane - 1]) || store.get().focusedTile;
        assignToTile(tile, id);
      } else if (context === "conversation") {
        if (item.saved) { await resumeSession(id); } else { openSession(id); }
        if (secondary) navigate("grid");
      } else {
        // mobile
        if (item.saved) { await resumeSession(id); } else { openSession(id); }
      }
    } catch (e) {
      addToast({ title: "Could not open session", detail: String(e.message || e), type: "error" });
      inFlightRef.current = false;
      return;
    }
    inFlightRef.current = false;
    onClose();
  }, [context, focusedPane, onClose]);

  // ── Create a session on the chosen dir (spec §5) ────────────────────────────
  const doCreate = useCallback(async (dir) => {
    const cwd = dir || createTarget;
    if (!cwd || creating || inFlightRef.current) return;
    inFlightRef.current = true;
    setCreating(true);
    try {
      const opts = { cwd };
      if (model) opts.model = model;
      await createSession(opts);
      onClose();
    } catch (e) {
      addToast({ title: "Could not create session", detail: String(e.message || e), type: "error" });
      setCreating(false);
      inFlightRef.current = false;
    }
  }, [createTarget, creating, model, onClose]);

  // Enter a directory in the create explorer.
  const goToDir = useCallback((path) => {
    setExploreDir(path);
    setDirFilter("");
    setQuery(tildify(path, homeDir));
    setSelectedIdx(0);
  }, [homeDir]);

  // Cycle the model chips (⌘M / click).
  const cycleModel = useCallback(() => {
    if (!models.length) return;
    const specs = models.map(modelSpec);
    const i = specs.indexOf(model);
    setModel(specs[(i + 1) % specs.length]);
  }, [models, model]);

  // Primary (⏎) / secondary (⌘⏎) verb on the current selection.
  const activateSelected = useCallback((secondary) => {
    const sel = selectable[selectedIdx];
    if (!sel) {
      // No selection in create with a navigated dir → ⌘⏎ still creates there.
      if (step === "create") doCreate();
      return;
    }
    if (sel.kind === "session") { activateSession(sel, secondary); return; }
    if (sel.kind === "action") { sel.run(); return; }
    if (sel.kind === "create-from-query") {
      // CORE fallback: open create with the query preserved (recents/browse
      // filter by it). NICE-TO-HAVE: send the query as the first message.
      // TODO 5H: create then sendMessage(newId, query).
      setStep("create");
      return;
    }
    if (sel.kind === "project") { doCreate(sel.cwd); return; }
    if (sel.kind === "dir") {
      if (secondary) doCreate(exploreDir); else goToDir(sel.path);
      return;
    }
  }, [selectable, selectedIdx, step, activateSession, doCreate, goToDir, exploreDir]);

  // create-step input handler (recents filter vs path explorer — ported from
  // NewSessionSheet.onInput).
  const onCreateInput = useCallback((v) => {
    setQuery(v);
    setSelectedIdx(0);
    const isPath = v.startsWith("/") || v.startsWith("~");
    if (!isPath) { setDirFilter(""); return; }
    const expanded = expandHome(v.replace(/\/+$/, ""), homeDir) || "/";
    if (v.endsWith("/")) {
      setExploreDir(expanded);
      setDirFilter("");
    } else {
      const cut = expanded.lastIndexOf("/");
      const parent = cut <= 0 ? "/" : expanded.slice(0, cut);
      setExploreDir(parent);
      setDirFilter(expanded.slice(cut + 1));
    }
  }, [homeDir]);

  const onKeyDown = useCallback((e) => {
    const meta = e.metaKey || e.ctrlKey;
    // Focus trap: Tab never leaves the palette (input always keeps focus).
    if (e.key === "Tab") { e.preventDefault(); return; }
    if (e.key === "Escape") { e.preventDefault(); onClose(); return; }
    if (meta && (e.key === "n" || e.key === "N") && step === "search") {
      e.preventDefault(); setStep("create"); return;
    }
    if (meta && (e.key === "m" || e.key === "M") && step === "create") {
      e.preventDefault(); cycleModel(); return;
    }
    if (e.key === "ArrowDown") {
      e.preventDefault();
      setSelectedIdx((i) => (selectable.length ? (i + 1) % selectable.length : 0));
      return;
    }
    if (e.key === "ArrowUp") {
      e.preventDefault();
      setSelectedIdx((i) => (selectable.length ? (i - 1 + selectable.length) % selectable.length : 0));
      return;
    }
    if (e.key === "ArrowRight" && step === "create") {
      const sel = selectable[selectedIdx];
      if (sel && sel.kind === "dir") { e.preventDefault(); goToDir(sel.path); return; }
    }
    if (e.key === "Enter") {
      e.preventDefault();
      activateSelected(meta);
      return;
    }
    if (e.key === "Backspace" && step === "create" && query === "") {
      e.preventDefault();
      if (initialStep === "create") onClose();
      else setStep("search");
      return;
    }
  }, [step, query, selectable, selectedIdx, onClose, cycleModel, goToDir, activateSelected, initialStep]);

  if (!open) return null;

  const activeDescId = selectable.length ? `pal-opt-${selectedIdx}` : undefined;
  const placeholder = step === "create"
    ? "Search a project or type a path…"
    : "Jump to session or type a command…";

  // Render a single selectable row, tracking its selectable index so hover/
  // click/aria line up with keyboard selection.
  let selCounter = -1;
  const rows = items.map((it, i) => {
    if (it.kind === "group") {
      return <div class="pal-group" role="presentation" key={`g${i}`}>{it.label}</div>;
    }
    if (it.kind === "note") {
      return <div class={`pal-note${it.error ? " err" : ""}`} key={`n${i}`}>{it.text}</div>;
    }
    selCounter += 1;
    const si = selCounter;
    const sel = si === selectedIdx && !isMobile;
    const common = {
      id: `pal-opt-${si}`,
      role: "option",
      "aria-selected": si === selectedIdx,
      "data-sel": si,
      class: `${isMobile ? "m-row" : "row"}${sel ? " sel" : ""}`,
      onMouseEnter: isMobile ? undefined : () => setSelectedIdx(si),
      onClick: () => { setSelectedIdx(si); requestAnimationFrame(() => activateSelectedFor(si)); },
    };
    if (it.kind === "session") {
      return (
        <div {...common} key={it.id}>
          <span class={`state-dot ${it.dotState}`} />
          <span class="name"><Highlight text={it.title} query={query.toLowerCase().trim()} /></span>
          {!isMobile && it.live && (
            <span class={`live${it.live.tone ? " " + it.live.tone : ""}`}>{it.live.text}</span>
          )}
          {it.archived && <span class="badge archived">archived</span>}
          {it.paneN && <span class="badge pane">P{it.paneN}</span>}
          <span class="cwd">{it.cwdLabel}</span>
          {it.when && <span class="when">{it.when}</span>}
          {!isMobile && <CornerDownLeft class="enter" size={14} />}
        </div>
      );
    }
    if (it.kind === "action") {
      return (
        <div {...common} key={it.id}>
          <span class={`act-ic${it.accent ? " " + it.accent : ""}`}>{it.icon}</span>
          <span class="act-name"><Highlight text={it.label} query={query.toLowerCase().trim()} /></span>
          {it.sublabel && !isMobile && <span class="act-sub">{it.sublabel}</span>}
          {it.shortcut && !isMobile && (
            <span class="shortcut">{it.shortcut.map((k, ki) => <kbd class="kbd" key={ki}>{k}</kbd>)}</span>
          )}
        </div>
      );
    }
    if (it.kind === "create-from-query") {
      return (
        <div {...common} key="cfq">
          <span class="act-ic"><Plus size={14} /></span>
          <span class="act-name">Start “{it.query}” as a new session</span>
          {!isMobile && <CornerDownLeft class="enter" size={14} />}
        </div>
      );
    }
    if (it.kind === "project") {
      return (
        <div {...common} key={it.cwd}>
          <span class="dir-ic"><FolderOpen size={14} /></span>
          <span class="path"><Highlight text={it.display} query={query.toLowerCase().trim()} /></span>
          {it.badge === "default" && <span class="badge default">server cwd</span>}
          {it.badge === "recent" && <span class="badge recent">recent</span>}
          {it.when && <span class="when">{it.when}</span>}
        </div>
      );
    }
    if (it.kind === "dir") {
      return (
        <div {...common} key={it.path}>
          <span class="dir-ic grey"><Folder size={14} /></span>
          <span class="path"><Highlight text={it.name} query={dirFilter.toLowerCase()} /></span>
          {!isMobile && <ChevronRight class="enter" size={14} />}
        </div>
      );
    }
    return null;
  });

  // click helper: activate the row whose selectable index is si.
  function activateSelectedFor(si) {
    const sel = selectable[si];
    if (!sel) return;
    if (sel.kind === "session") { activateSession(sel, false); return; }
    if (sel.kind === "action") { sel.run(); return; }
    if (sel.kind === "create-from-query") { setStep("create"); return; }
    if (sel.kind === "project") { doCreate(sel.cwd); return; }
    if (sel.kind === "dir") { goToDir(sel.path); return; }
  }

  // Footer verb hints (spec §4) — desktop only.
  let primaryHint = "open";
  let secondaryHint = null;
  if (step === "search") {
    if (context === "grid") {
      primaryHint = focusedPane != null ? `→ pane ${focusedPane}` : "→ pane";
      secondaryHint = "open full";
    } else if (context === "conversation") {
      secondaryHint = "open in pane";
    }
  }

  const onVeil = (e) => { if (e.target === e.currentTarget) onClose(); };

  // ── Mobile chassis (bottom sheet) ───────────────────────────────────────────
  // TODO 5L (overlay-history hook): the palette doesn't use Sheet — it has its
  // own two chassis (mobile bottom sheet / desktop centered veil) and Escape
  // here doesn't always close (it steps back from the "create" step first,
  // see onKeyDown above), so wiring it to data/overlay-history.js needs a bit
  // more care than a drop-in openOverlay() call to keep that step-back
  // behavior correct on the back gesture too. Left out of this pass; the
  // Sheet-based overlays (RewindTimeline, file/HTML viewers, drawers) already
  // get the back-gesture hook via Sheet.
  if (isMobile) {
    return (
      <div class="pal-veil pal-veil-mobile" onClick={onVeil}>
        <div
          class="m-sheet"
          role="dialog"
          aria-modal="true"
          aria-label="Command palette"
          onKeyDown={onKeyDown}
        >
          <div class="grab" aria-hidden="true" />
          <div class="m-input-row">
            {step === "create"
              ? <button type="button" class="crumb-chip" onClick={() => { setStep("search"); inputRef.current?.focus(); }}><ArrowLeft size={11} /> New session</button>
              : <Search size={16} aria-hidden="true" />}
            <input
              ref={inputRef}
              class="pal-input"
              type="text"
              role="combobox"
              aria-expanded="true"
              aria-controls="pal-listbox"
              aria-activedescendant={activeDescId}
              autocomplete="off" autocapitalize="off" spellcheck={false}
              placeholder={placeholder}
              value={query}
              onInput={(e) => (step === "create" ? onCreateInput(e.target.value) : (setQuery(e.target.value), setSelectedIdx(0)))}
            />
          </div>
          <div class="m-list" id="pal-listbox" role="listbox" aria-label="Results" ref={listRef}>
            {rows}
          </div>
          {step === "create" && (
            <>
              <div class="field-row">
                <span class="lbl">Model</span>
                <div class="model-chips">
                  {models.map((m) => {
                    const spec = modelSpec(m);
                    return (
                      <button type="button" key={spec} class={`mchip${spec === model ? " on" : ""}`} onClick={() => setModel(spec)}>
                        {m.name || m.id}
                      </button>
                    );
                  })}
                </div>
              </div>
              <div class="create-bar">
                <button type="button" class="btn-create" disabled={!createTarget || creating} onClick={() => doCreate()}>
                  {creating ? "Creating…" : `Create in ${basename(createTarget) || "…"}`}
                </button>
              </div>
            </>
          )}
        </div>
      </div>
    );
  }

  // ── Desktop chassis (centered overlay) ──────────────────────────────────────
  return (
    <div class="pal-veil" onClick={onVeil}>
      <div
        class="palette"
        role="dialog"
        aria-modal="true"
        aria-label="Command palette"
        onKeyDown={onKeyDown}
      >
        <div class="pal-input-row">
          {step === "create"
            ? <button type="button" class="crumb-chip" onClick={() => { setStep("search"); inputRef.current?.focus(); }}><ArrowLeft size={12} /> New session</button>
            : <Search size={16} aria-hidden="true" />}
          <input
            ref={inputRef}
            class="pal-input"
            type="text"
            role="combobox"
            aria-expanded="true"
            aria-controls="pal-listbox"
            aria-activedescendant={activeDescId}
            autocomplete="off" autocapitalize="off" spellcheck={false}
            placeholder={placeholder}
            value={query}
            onInput={(e) => (step === "create" ? onCreateInput(e.target.value) : (setQuery(e.target.value), setSelectedIdx(0)))}
          />
          <kbd class="kbd">esc</kbd>
        </div>

        <div class="pal-list" id="pal-listbox" role="listbox" aria-label="Results" ref={listRef}>
          {rows}
        </div>

        {step === "create" && (
          <div class="field-row">
            <span class="lbl">Model</span>
            <div class="model-chips">
              {models.map((m) => {
                const spec = modelSpec(m);
                return (
                  <button type="button" key={spec} class={`mchip${spec === model ? " on" : ""}`} onClick={() => setModel(spec)}>
                    {m.name || m.id}
                  </button>
                );
              })}
            </div>
            <kbd class="kbd">{modLabel}M cycle</kbd>
          </div>
        )}

        {step === "create" ? (
          <div class="create-bar">
            <span class="cancel"><kbd class="kbd">⌫</kbd> back on empty query</span>
            <button type="button" class="btn-create" disabled={!createTarget || creating} onClick={() => doCreate()}>
              {creating ? "Creating…" : `Create in ${basename(createTarget) || "…"}`}
              {!creating && <kbd class="kbd">{modLabel}⏎</kbd>}
            </button>
          </div>
        ) : (
          <div class="pal-foot">
            <span class="f"><kbd class="kbd">↑↓</kbd> navigate</span>
            <span class="f"><kbd class="kbd">⏎</kbd> {primaryHint}</span>
            {secondaryHint && <span class="f"><kbd class="kbd">{modLabel}⏎</kbd> {secondaryHint}</span>}
            <span class="spring" />
            <span class="ctxhint" aria-live="polite">
              {context === "grid" && focusedPane != null ? `grid · pane ${focusedPane} focused` : `${selectable.length} result${selectable.length === 1 ? "" : "s"}`}
            </span>
          </div>
        )}
      </div>
    </div>
  );
}
