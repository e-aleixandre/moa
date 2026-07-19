import { useState, useEffect } from "preact/hooks";
import { Spine } from "../Spine/Spine.jsx";
import { GridToolbar } from "../GridToolbar/GridToolbar.jsx";
import { PaneGrid } from "../PaneGrid/PaneGrid.jsx";
import { store } from "../../data/store.js";
import { allTileIds, allSessionIds, findTile, tileCount, treeShape, presetTree } from "../../data/tileTree.js";
import { PRESETS } from "../../data/layoutPresets.js";
import { applyPreset, addPane, focusTileByIndex, focusTile, openSession } from "../../data/tile-actions.js";
import { openPalette } from "../../data/palette.js";
import "./PaneGridScreen.css";

// PaneGridScreen — root organism AND container of the desktop pane grid (5G).
// Same 5C container pattern as ConversationScreen: subscribes to the store,
// derives sessions/tileTree/focusedTile, and passes real props down to the
// presentational Spine / GridToolbar / PaneGrid. Owns the grid-only global
// keyboard shortcuts (⌘/Alt+1–9 → focus pane N).

// relAge / spineSessions — same coarse relative-age + ACTIVE/SAVED split as
// ConversationScreen (kept local; a shared helper is a later cleanup).
function relAge(updated) {
  if (!updated) return "";
  const diff = Date.now() - updated;
  const min = Math.floor(diff / 60000);
  if (min < 1) return "now";
  if (min < 60) return `${min}m`;
  const h = Math.floor(min / 60);
  if (h < 24) return `${h}h`;
  const d = Math.floor(h / 24);
  return `${d}d`;
}

function spineSessions(sessions, paneOf) {
  const all = Object.values(sessions).filter((s) => !s.archived);
  const active = all
    .filter((s) => s.state !== "saved")
    .sort((a, b) => (b.updated || 0) - (a.updated || 0))
    .map((s) => ({
      id: s.id,
      title: s.title || s.id,
      state: s.state || "idle",
      unseen: !!s.unseen,
      meta: relAge(s.updated),
      pane: paneOf.get(s.id) || undefined,
    }));
  const saved = all
    .filter((s) => s.state === "saved")
    .sort((a, b) => (b.updated || 0) - (a.updated || 0))
    .map((s) => ({ id: s.id, title: s.title || s.id, meta: relAge(s.updated) }));
  return { active, saved };
}

// matchPreset returns the preset id whose tree shape equals the current layout,
// or null (freeform — no preset highlighted). Compares structure only (splits +
// directions), ignoring ids/sessions/ratios.
function matchPreset(tree) {
  const shape = treeShape(tree);
  for (const p of PRESETS) {
    if (treeShape(presetTree(p.id)) === shape) return p.id;
  }
  return null;
}

export function PaneGridScreen({ version }) {
  const [state, setState] = useState(store.get());
  useEffect(() => store.subscribe(setState), []);

  // sessionId → pane index+1 (DFS order), for the Spine's Pn badges.
  const paneOf = new Map();
  for (const [i, tileId] of allTileIds(state.tileTree).entries()) {
    const tile = findTile(state.tileTree, tileId);
    if (tile && tile.sessionId) paneOf.set(tile.sessionId, `P${i + 1}`);
  }

  const { active, saved } = spineSessions(state.sessions, paneOf);
  const paneCount = tileCount(state.tileTree);
  const activePreset = matchPreset(state.tileTree);
  const focusedSessionId = (() => {
    const t = findTile(state.tileTree, state.focusedTile);
    return t ? t.sessionId : null;
  })();

  // needsYouCount — sessions currently assigned to a visible pane that need
  // attention (permission/error). Derived from the store (no /api/attention in
  // 5G — kept simple, per the plan).
  const assigned = new Set(allSessionIds(state.tileTree));
  const needsYou = Object.values(state.sessions).filter(
    (s) => assigned.has(s.id) && (s.state === "permission" || s.state === "error")
  );

  // onAttentionClick — focus the pane holding the first session that needs you.
  const onAttentionClick = () => {
    if (needsYou.length === 0) return;
    const target = needsYou[0];
    for (const tileId of allTileIds(state.tileTree)) {
      const t = findTile(state.tileTree, tileId);
      if (t && t.sessionId === target.id) { focusTile(tileId); return; }
    }
  };

  // Grid hotkeys: ⌘/Ctrl/Alt + 1–9 focus pane N. We require a modifier chord
  // so a bare number typed into a pane's composer goes to the textarea, never
  // the layout (⌘2 is unambiguous — never a literal "2").
  useEffect(() => {
    const onKey = (e) => {
      if (e.key < "1" || e.key > "9") return;
      if (!(e.metaKey || e.ctrlKey || e.altKey)) return;
      e.preventDefault();
      focusTileByIndex(parseInt(e.key, 10) - 1);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  const onSelectSession = (id) => { openSession(id); };

  return (
    <div class="pane-grid-screen">
      <Spine
        version={version}
        activeSessions={active}
        savedSessions={saved}
        activeId={focusedSessionId}
        onSelectSession={onSelectSession}
        onNewSession={() => openPalette("create")}
        onSearch={() => openPalette("search")}
        onSettings={() => { /* 5x: settings */ }}
      />
      <main class="pane-grid-main">
        <GridToolbar
          paneCount={paneCount}
          activePreset={activePreset}
          needsYouCount={needsYou.length}
          onAttentionClick={onAttentionClick}
          onPresetSelect={(id) => applyPreset(id)}
          onSplitRight={() => addPane("horizontal")}
          onSplitDown={() => addPane("vertical")}
        />
        <PaneGrid state={state} />
      </main>
    </div>
  );
}
