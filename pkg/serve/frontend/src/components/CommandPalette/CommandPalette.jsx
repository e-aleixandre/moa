import { useState, useEffect, useMemo, useRef, useCallback } from "preact/hooks";
import {
  Search, Plus, LayoutGrid, MessageSquare, CornerDownLeft,
  ArrowLeft, Folder, FolderOpen, ChevronRight, Smartphone, Check,
} from "lucide-preact";
import { store } from "../../data/store.js";
import { api } from "../../data/api.js";
import { closePalette } from "../../data/palette.js";
import { openDrawer } from "../../data/drawer.js";
import { openPulsePairing } from "../../data/pulse-pairing-panel.js";
import { fuzzyMatch, fuzzyMatchIndices } from "../../data/fuzzy.js";
import {
  createSession, resumeSession,
} from "../../data/session-actions.js";
import { assignToTile, openSession } from "../../data/tile-actions.js";
import { navigate } from "../../data/router.js";
import { allTileIds, findTile } from "../../data/tileTree.js";
import { addToast } from "../../data/notifications.js";
import {
  sessionTitle, sessionDisplayDotState, isRecentSession, projectLabel,
  tildify, expandHome, basename,
} from "../../data/util/format.js";
import { sessionSearchMatch } from "../../data/util/project-sessions.js";
import { modLabel } from "../../data/util/shortcut.js";
import { deriveModelSpecs } from "../../data/selectors.js";
import { defaultModelSpec, modelStepItems, stepBack } from "./command-palette-model.js";
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
// Pinned models come from the same preference the ModelSelector's stars write
// (/api/model-preferences), so the palette's model step opens on the user's
// go-to models instead of a second, palette-only notion of "favourite". Cached
// like caps/models: read-only here — pinning stays in the ModelSelector.
let _pinnedIDs = null;
function getPinnedIDs() {
  if (_pinnedIDs) return Promise.resolve(_pinnedIDs);
  return api("GET", "/api/model-preferences")
    .then((prefs) => { _pinnedIDs = prefs?.pinned_models || []; return _pinnedIDs; })
    .catch(() => []);
}

// ── Pure path helpers — ported verbatim from the old SPA's NewSessionSheet.
// tildify/expandHome/basename moved to data/util/format.js once the mobile
// drawer's new-session screen needed the same ~ handling; parentDir and
// truncMiddle stay here because only the palette's rows use them.
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

// CommandPalette — the ⌘K palette. One ranked list of sessions + actions
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
  // Mirrors `model` for the async default seeding, which must read the value
  // as of its resolution, not as of the effect that started it.
  const modelRef = useRef("");
  modelRef.current = model;
  const [pinnedIDs, setPinnedIDs] = useState(_pinnedIDs || []);
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
  // Set when the model step is opened so the cursor can land on the current
  // model once its rows exist (see the effect below).
  const syncModelSelectionRef = useRef(false);
  // The create-step query parked while the model step is open (see goToModel).
  const createQueryRef = useRef("");
  const homeDir = caps.homeDir || "";
  const serverCwd = caps.workspaceRoot || "";
  const isMobile = context === "mobile";

  // On open: remember the opener (to restore focus on close), reset transient
  // state, fetch caps, and focus the input next frame (spec §6/§9).
  useEffect(() => {
    if (!open) return;
    openerRef.current = document.activeElement;
    // The create step doesn't exist on a phone any more (it lives in the
    // SessionDrawer), so an open asking for it hands over instead.
    if (isMobile && initialStep === "create") {
      onClose();
      openDrawer("new");
      return;
    }
    setStep(initialStep);
    setQuery("");
    setSelectedIdx(0);
    setCreating(false);
    inFlightRef.current = false;
    setBrowseErr(false);
    getCaps().then(setCaps);
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
  // model, refocus the (now cleared) input. Coming BACK from the model step is
  // not an entry — the browsed directory the user already picked must survive
  // choosing a model.
  const prevStepRef = useRef(initialStep);
  useEffect(() => {
    const cameFromModel = prevStepRef.current === "model";
    prevStepRef.current = step;
    if (!open || step !== "create" || cameFromModel) return;
    setQuery("");
    setSelectedIdx(0);
    setExploreDir(serverCwd || homeDir || "/");
    setDirFilter("");
    // Seed the default from BOTH answers at once: the server's default model
    // is in caps, but only the catalogue can tell whether that spec exists, and
    // whichever request lands second must not overwrite an explicit choice —
    // hence modelRef instead of the value captured by this effect.
    Promise.all([getCaps(), getModels()]).then(([c, m]) => {
      setModels(m);
      if (modelRef.current) return;
      const def = defaultModelSpec(c, deriveModelSpecs(m));
      if (def) setModel(def);
    });
    requestAnimationFrame(() => inputRef.current?.focus());
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, step]);

  // Entering the model step: the pinned list is the only datum create doesn't
  // already have.
  useEffect(() => {
    if (!open || step !== "model") return;
    getPinnedIDs().then(setPinnedIDs);
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

  // goToCreate — the ONE way into "new session" from the palette. On desktop
  // that is the palette's own create step. On a phone it hands over to the
  // SessionDrawer's "new" screen instead: the drawer is the single place a
  // phone manages sessions, and opening a second create flow inside another
  // chassis (with its own session list and its own back) was exactly the
  // duplication this removes.
  const goToCreate = useCallback(() => {
    if (isMobile) { onClose(); openDrawer("new"); return; }
    setStep("create");
  }, [isMobile, onClose]);

  // ── Actions catalogue (context-aware). CORE set only; NICE-TO-HAVE presets
  // left as TODO (spec §3).
  const actions = useMemo(() => {
    const list = [];
    list.push({
      id: "__new", label: "New session…", sublabel: "pick project & model",
      icon: <Plus size={14} />, accent: "", shortcut: [modLabel, "N"],
      run: goToCreate,
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
    // Pair Pulse — opens the QR pairing panel. Available in every context;
    // pairing is a device-wide action, not session- or view-scoped.
    list.push({
      id: "__pair-pulse", label: "Pair Pulse…", sublabel: "connect a phone via QR",
      icon: <Smartphone size={14} />, accent: "", shortcut: null,
      run: () => { onClose(); openPulsePairing(); },
    });
    // TODO nice-to-have: Layout preset actions (grid only), Archive current,
    // Settings — cheap via this action.run() pattern, left out to keep CORE tight.
    return list;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [context, onClose, goToCreate]);

  // ── Build the flat item list for the SEARCH step ───────────────────────────
  const searchItems = useMemo(() => {
    const q = query.toLowerCase().trim();
    const out = [];

    // Sessions (MRU). No query → recent, capped. Session rows use the same
    // word matcher as both session lists: sentence-length titles make command
    // fuzzy matching return distracting subsequence false positives. Actions
    // below deliberately retain their established command-palette fuzzy match.
    const all = Object.values(state.sessions).sort((a, b) => (b.updated || 0) - (a.updated || 0));
    const sessRows = [];
    for (const sess of all) {
      const cwd = sess.cwd || "";
      const cwdLabel = projectLabel(cwd);
      const title = sessionTitle(sess);
      if (q) {
        if (!sessionSearchMatch(query, { ...sess, title, path: cwdLabel })) continue;
      } else {
        if (!isRecentSession(sess)) continue;
      }
      sessRows.push({
        kind: "session",
        id: sess.id,
        title,
        dotState: sessionDisplayDotState(sess),
        live: liveStatus(sess),
        cwdLabel,
        when: relativeWhen(sess.updated),
        paneN: paneOf(state.tileTree, sess.id),
        saved: sess.state === "saved",
      });
    }
    const cappedSessions = q ? sessRows : sessRows.slice(0, CAP_NO_QUERY);
    if (cappedSessions.length) {
      out.push({ kind: "group", label: q ? "Sessions" : "Recent" });
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

  // ── Build the flat item list for the MODEL step ────────────────────────────
  // Same rows/keyboard/search as every other step — the model choice is a step
  // of the palette, not a selector embedded inside it. Pinned first, then one
  // group per provider (ModelSelector's own reading order); a query filters
  // with its matcher (codename / name / alias / provider).
  const modelSpecs = useMemo(() => deriveModelSpecs(models), [models]);
  const modelItems = useMemo(
    () => modelStepItems(modelSpecs, pinnedIDs, query),
    [modelSpecs, pinnedIDs, query],
  );

  const items = step === "create" ? createItems : step === "model" ? modelItems : searchItems;

  // Selectable indices (skip groups / notes). selectedIdx indexes into this.
  const selectable = useMemo(
    () => items.filter((it) => it.kind !== "group" && it.kind !== "note"),
    [items],
  );

  // Entering the model step lands on the model already selected, so ⏎ is a
  // no-op instead of a silent change. It re-runs while the list is still
  // settling (the pinned preference arrives after the first paint and reorders
  // the rows) and stops the moment the user takes over the cursor by typing,
  // navigating or hovering.
  useEffect(() => {
    if (step !== "model" || !syncModelSelectionRef.current || !selectable.length) return;
    const at = selectable.findIndex((it) => it.spec?.id === model);
    setSelectedIdx(at > 0 ? at : 0);
  }, [step, selectable, model]);

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
    // Saved sessions auto-resume once visible (afterVisibilityChange,
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

  // Open the model step (Change / ⌘M): the palette's own list + search, not a
  // second selector chrome on top of it. The create step's query is kept aside
  // so returning lands on the very project/directory it was left on.
  const goToModel = useCallback(() => {
    createQueryRef.current = query;
    setQuery("");
    setSelectedIdx(0);
    syncModelSelectionRef.current = true;
    setStep("model");
    requestAnimationFrame(() => inputRef.current?.focus());
  }, [query]);

  // Back to create with its query (and therefore its browsed directory)
  // restored — shared by choosing a model and by backing out of the step.
  const backToCreate = useCallback(() => {
    setQuery(createQueryRef.current);
    setSelectedIdx(0);
    setStep("create");
    requestAnimationFrame(() => inputRef.current?.focus());
  }, []);

  // Choose a model and return to create — the selection is the whole point of
  // the step, so there is nothing else to confirm.
  const chooseModel = useCallback((spec) => {
    setModel(spec);
    backToCreate();
  }, [backToCreate]);

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
      // TODO: create then sendMessage(newId, query).
      goToCreate();
      return;
    }
    if (sel.kind === "project") { doCreate(sel.cwd); return; }
    if (sel.kind === "model") { chooseModel(sel.spec.id); return; }
    if (sel.kind === "dir") {
      if (secondary) doCreate(exploreDir); else goToDir(sel.path);
      return;
    }
  }, [selectable, selectedIdx, step, activateSession, doCreate, goToDir, exploreDir, goToCreate, chooseModel]);

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

  // Input handler shared by both chassis: the create step has its own path/
  // recents parsing, every other step is a plain filter. Typing always hands
  // the cursor back to "first hit".
  const onInput = useCallback((v) => {
    syncModelSelectionRef.current = false;
    if (step === "create") { onCreateInput(v); return; }
    setQuery(v);
    setSelectedIdx(0);
  }, [step, onCreateInput]);

  // goBack — one back gesture for Escape, ⌫-on-empty-query and the crumb
  // chip, so all three agree on where a step returns to (stepBack).
  const goBack = useCallback(() => {
    const to = stepBack(step, initialStep);
    if (to === "close") { onClose(); return; }
    if (to === "create") { backToCreate(); return; }
    setQuery("");
    setSelectedIdx(0);
    setStep(to);
    requestAnimationFrame(() => inputRef.current?.focus());
  }, [step, initialStep, onClose, backToCreate]);

  const onKeyDown = useCallback((e) => {
    const meta = e.metaKey || e.ctrlKey;
    // Focus trap: Tab never leaves the palette (input always keeps focus).
    if (e.key === "Tab") { e.preventDefault(); return; }
    if (e.key === "Escape") {
      e.preventDefault();
      // The model step is a sub-step: Escape backs out of it (keeping the
      // create it was opened from) instead of throwing the whole flow away.
      if (step === "model") goBack(); else onClose();
      return;
    }
    if (meta && (e.key === "n" || e.key === "N") && step === "search") {
      e.preventDefault(); goToCreate(); return;
    }
    if (meta && (e.key === "m" || e.key === "M") && step === "create") {
      e.preventDefault(); goToModel(); return;
    }
    if (e.key === "ArrowDown") {
      e.preventDefault();
      syncModelSelectionRef.current = false;
      setSelectedIdx((i) => (selectable.length ? (i + 1) % selectable.length : 0));
      return;
    }
    if (e.key === "ArrowUp") {
      e.preventDefault();
      syncModelSelectionRef.current = false;
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
    if (e.key === "Backspace" && (step === "create" || step === "model") && query === "") {
      e.preventDefault();
      goBack();
      return;
    }
  }, [step, query, selectable, selectedIdx, onClose, goToModel, goToDir, activateSelected, goBack, goToCreate]);

  if (!open) return null;

  const activeDescId = selectable.length ? `pal-opt-${selectedIdx}` : undefined;
  const placeholder = step === "create"
    ? "Search a project or type a path…"
    : step === "model"
      ? "Search models…"
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
      onMouseEnter: isMobile ? undefined : () => { syncModelSelectionRef.current = false; setSelectedIdx(si); },
      onClick: () => { setSelectedIdx(si); requestAnimationFrame(() => activateSelectedFor(si)); },
    };
    if (it.kind === "session") {
      if (isMobile) {
        return (
          <div {...common} key={it.id}>
            <span class={`state-dot ${it.dotState}`} />
            <span class="name"><Highlight text={it.title} query={query.toLowerCase().trim()} /></span>
            <span class="cwd">{it.cwdLabel}</span>
            {it.when && <span class="when">{it.when}</span>}
          </div>
        );
      }
      const live = it.live?.text;
      const useful = live && live !== "idle" && live !== "saved";
      const sub = useful ? live : (it.cwdLabel || "");
      return (
        <div {...common} key={it.id}>
          <span class={`state-dot ${it.dotState}`} />
          <span class="pal-sess">
            <span class="name"><Highlight text={it.title} query={query.toLowerCase().trim()} /></span>
            {sub && (
              <span class={`live${it.live?.tone ? " " + it.live.tone : ""}`}>{sub}</span>
            )}
          </span>
          {it.paneN && <span class="badge pane">P{it.paneN}</span>}
          {it.when && <span class="when">{it.when}</span>}
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
    if (it.kind === "model") {
      const spec = it.spec;
      const on = spec.id === model;
      return (
        <div {...common} key={spec.id}>
          <span class="model-dot" style={{ background: `var(--${spec.accent})` }} />
          <span class="act-name" style={{ color: `var(--${spec.accent})` }}>
            <Highlight text={spec.codename} query={query.toLowerCase().trim()} />
          </span>
          {spec.sub && !isMobile && <span class="act-sub">{spec.sub}</span>}
          <span class="badge provider">{spec.provider}</span>
          {on && <Check class="model-check" size={14} />}
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
    if (sel.kind === "create-from-query") { goToCreate(); return; }
    if (sel.kind === "project") { doCreate(sel.cwd); return; }
    if (sel.kind === "model") { chooseModel(sel.spec.id); return; }
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
  } else if (step === "model") {
    primaryHint = "use model";
  }

  const onVeil = (e) => { if (e.target === e.currentTarget) onClose(); };

  // The model row's copy: the catalogued spec of the current selection when we
  // know it, else the raw spec string a server default may carry before the
  // catalogue arrives (never an empty row).
  const currentModelSpec = modelSpecs.find((spec) => spec.id === model);
  const currentModelLabel = currentModelSpec?.codename || model.split("/").pop() || "default";

  // Back from the "create" step — same rule the Backspace shortcut already
  // follows: step back to search only if that is where we CAME FROM. Opened
  // straight into create (the drawer's +), search is not a previous screen, and
  // falling into it would drop the user in a second session list they never
  // asked for.
  const onCrumbBack = () => {
    if (initialStep === "create") { onClose(); return; }
    setStep("search");
    inputRef.current?.focus();
  };

  // ── Mobile chassis (bottom sheet) ───────────────────────────────────────────
  // TODO (overlay-history hook): the palette doesn't use Sheet — it has its
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
              ? <button type="button" class="crumb-chip" onClick={onCrumbBack}><ArrowLeft size={11} /> New session</button>
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
              onInput={(e) => onInput(e.target.value)}
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
          {step === "search"
            ? <Search size={16} aria-hidden="true" />
            : (
              <button type="button" class="crumb-chip" onClick={goBack}>
                <ArrowLeft size={12} /> {step === "model" ? "Model" : "New session"}
              </button>
            )}
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
            onInput={(e) => onInput(e.target.value)}
          />
          <kbd class="kbd">esc</kbd>
        </div>

        <div class="pal-list" id="pal-listbox" role="listbox" aria-label="Results" ref={listRef}>
          {rows}
        </div>

        {step === "create" && (
          <div class="field-row model-row">
            <span class="lbl">Model</span>
            <span class="model-current" style={currentModelSpec ? { color: `var(--${currentModelSpec.accent})` } : undefined}>
              {currentModelLabel}
            </span>
            {currentModelSpec?.sub && <span class="model-sub">{currentModelSpec.sub}</span>}
            <button type="button" class="model-change" onClick={goToModel}>
              Change <kbd class="kbd">{modLabel}M</kbd>
            </button>
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
            {step === "model" && <span class="f"><kbd class="kbd">⌫</kbd> back</span>}
            <span class="spring" />
            <span class="ctxhint" aria-live="polite">
              {context === "grid" && focusedPane != null && step === "search"
                ? `grid · pane ${focusedPane} focused`
                : `${selectable.length} result${selectable.length === 1 ? "" : "s"}`}
            </span>
          </div>
        )}
      </div>
    </div>
  );
}
