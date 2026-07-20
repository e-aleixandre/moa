import { useCallback, useMemo, useRef, useState, useEffect } from "preact/hooks";
import { MessageSquarePlus } from "lucide-preact";
import { Pane } from "../Pane/Pane.jsx";
import { Stream } from "../Stream/Stream.jsx";
import { Composer } from "../Composer/Composer.jsx";
import { McpBanner, PermissionPrompt, AskUserPrompt } from "../../components/index.js";
import { snapToRatio } from "../../data/snap.js";
import { formatShortcut } from "../../data/util/shortcut.js";
import {
  resizeSplit, assignToTile, swapTiles, splitTile, closeTile, focusTile,
} from "../../data/tile-actions.js";
import { navigate } from "../../data/router.js";
import { allTileIds } from "../../data/tileTree.js";
import { getTileCount } from "../../data/store.js";
import { projectStream } from "../../data/stream-model.js";
import { modelAccent } from "../../data/selectors.js";
import { shortModel, shortPath, sessionDotState, modelCodename, sessionTitle } from "../../data/util/format.js";
import { useTouchDrag, registerDropTarget } from "../../hooks/useTouchDrag.js";
import "./PaneGrid.css";

// PaneGrid — 5G. Renders the REAL binary split tree (state.tileTree) recursively:
// a split node becomes two flex split-panes with a ResizeHandle between them; a
// leaf becomes a ConnectedPane (session's live Stream + Composer, or an empty
// dropzone). The old SPA's TileTree.jsx/Tile.jsx are ported 1:1, retargeted to
// the next's Pane/Stream/Composer.

// ResizeHandle — pointer-driven splitter. Ported verbatim from the old SPA:
// pointerdown captures the pointer, pointermove maps the cursor position to a
// fraction of the split and snaps it (snapToRatio) into resizeSplit(path,ratio),
// pointerup cleans up.
function ResizeHandle({ path, direction }) {
  const isH = direction === "horizontal";
  // Holds the teardown for an in-flight drag so it can also run on unmount.
  const cleanupRef = useRef(null);

  const onPointerDown = useCallback((e) => {
    e.preventDefault();
    const handle = e.currentTarget;
    const parent = handle.parentElement;
    const rect = parent.getBoundingClientRect();
    const pointerId = e.pointerId;

    handle.setPointerCapture(pointerId);
    handle.classList.add("active");
    document.body.style.cursor = isH ? "col-resize" : "row-resize";

    const onPointerMove = (ev) => {
      const pos = isH ? ev.clientX - rect.left : ev.clientY - rect.top;
      const total = isH ? rect.width : rect.height;
      const pct = Math.max(0.15, Math.min(0.85, pos / total));
      resizeSplit([...path], snapToRatio(pct));
    };

    // endResize — idempotent teardown. Runs on pointerup, and also on
    // pointercancel / lostpointercapture (touch interruption, focus loss) so
    // the cursor, the 'active' class and the native listeners never get stuck.
    const endResize = () => {
      if (cleanupRef.current !== endResize) return; // already torn down
      cleanupRef.current = null;
      handle.classList.remove("active");
      document.body.style.cursor = "";
      handle.removeEventListener("pointermove", onPointerMove);
      handle.removeEventListener("pointerup", onPointerUp);
      handle.removeEventListener("pointercancel", endResize);
      handle.removeEventListener("lostpointercapture", endResize);
      try { handle.releasePointerCapture(pointerId); } catch (_) { /* already released */ }
    };
    const onPointerUp = () => endResize();

    handle.addEventListener("pointermove", onPointerMove);
    handle.addEventListener("pointerup", onPointerUp);
    handle.addEventListener("pointercancel", endResize);
    handle.addEventListener("lostpointercapture", endResize);
    cleanupRef.current = endResize;
  }, [path, isH]);

  // If the handle unmounts mid-drag (e.g. a preset change), tear down.
  useEffect(() => () => { if (cleanupRef.current) cleanupRef.current(); }, []);

  return (
    <div
      class={`resize-handle ${isH ? "resize-h" : "resize-v"}`}
      onPointerDown={onPointerDown}
    />
  );
}

// ConnectedPane — a leaf tile bound to a real session (or empty). Wires the
// Pane's optional 5G props to the tile actions and mounts the live Stream +
// Composer (+ blocking) when a session is assigned.
function ConnectedPane({ node, state, tileIndex }) {
  const tileId = node.id;
  const session = node.sessionId ? state.sessions[node.sessionId] : null;
  const focused = state.focusedTile === tileId;
  const [dragOver, setDragOver] = useState(false);
  const paneRef = useRef(null);
  const canClose = getTileCount() > 1;
  const attention = session && (session.state === "permission" || session.state === "error");

  // --- HTML5 drag source (desktop) ---
  const handleDragStart = useCallback((e) => {
    e.dataTransfer.setData("text/x-tile-id", String(tileId));
    if (node.sessionId) e.dataTransfer.setData("text/x-session-id", node.sessionId);
    e.dataTransfer.effectAllowed = "move";
    const el = paneRef.current;
    if (el) {
      const rect = el.getBoundingClientRect();
      const ghost = el.cloneNode(true);
      ghost.style.width = rect.width + "px";
      ghost.style.height = rect.height + "px";
      ghost.style.position = "fixed";
      ghost.style.top = "-9999px";
      ghost.style.opacity = "0.85";
      ghost.style.borderRadius = "8px";
      ghost.style.overflow = "hidden";
      document.body.appendChild(ghost);
      e.dataTransfer.setDragImage(ghost, e.clientX - rect.left, e.clientY - rect.top);
      requestAnimationFrame(() => ghost.remove());
    }
  }, [tileId, node.sessionId]);

  const handleDragOver = useCallback((e) => {
    e.preventDefault();
    e.dataTransfer.dropEffect = "move";
    setDragOver(true);
  }, []);

  const handleDragLeave = useCallback(() => setDragOver(false), []);

  const applyDrop = useCallback((fromTileId, sid) => {
    if (fromTileId) {
      swapTiles(parseInt(fromTileId, 10), tileId);
      return;
    }
    if (sid) assignToTile(tileId, sid);
  }, [tileId]);

  const handleDrop = useCallback((e) => {
    e.preventDefault();
    setDragOver(false);
    applyDrop(e.dataTransfer.getData("text/x-tile-id"), e.dataTransfer.getData("text/x-session-id"));
  }, [applyDrop]);

  // --- Touch drag source + drop target ---
  const touchDrag = useTouchDrag({
    data: { "text/x-tile-id": String(tileId), "text/x-session-id": node.sessionId || "" },
  });

  useEffect(() => {
    const el = paneRef.current;
    if (!el) return;
    return registerDropTarget(el, {
      onDragOver: () => setDragOver(true),
      onDragLeave: () => setDragOver(false),
      onDrop: (data) => {
        setDragOver(false);
        applyDrop(data["text/x-tile-id"], data["text/x-session-id"]);
      },
    });
  }, [applyDrop]);

  // --- Click-to-focus (ported from Tile.handleTileClick) ---
  const handleFocus = useCallback((e) => {
    const t = e.target;
    if (t && t.closest && t.closest('input, textarea, [contenteditable="true"], .ask-user-card, .composer')) {
      focusTile(tileId, { focusInput: false });
      return;
    }
    focusTile(tileId, { respectSelection: true });
  }, [tileId]);

  // --- Maximize → back to conversation view with this session focused ---
  const handleMaximize = useCallback(() => {
    if (!node.sessionId) return;
    // Keep the session in the focused tile, then leave the grid in place: the
    // router flips the view (pushState, no reload) and the conversation screen
    // renders the focused tile's session. navigate({session}) focuses it first.
    assignToTile(tileId, node.sessionId);
    navigate(null, { session: node.sessionId });
  }, [tileId, node.sessionId]);

  const commonProps = {
    paneRef,
    dataTileId: tileId,
    tileNumber: tileIndex + 1,
    focused,
    dragOver,
    canClose,
    draggable: true,
    onDragStart: handleDragStart,
    touchDrag,
    onDragOver: handleDragOver,
    onDragLeave: handleDragLeave,
    onDrop: handleDrop,
    onFocus: handleFocus,
    onSplitRight: (e) => { e.stopPropagation(); splitTile(tileId, "horizontal"); },
    onSplitDown: (e) => { e.stopPropagation(); splitTile(tileId, "vertical"); },
    onClose: (e) => { e.stopPropagation(); closeTile(tileId); },
  };

  if (!session) {
    return (
      <Pane
        {...commonProps}
        title="Empty"
        state="idle"
        empty
        hideComposer
      >
        <div class="pane-empty">
          <MessageSquarePlus aria-hidden="true" />
          <span class="pane-empty-title">Drag a session here</span>
          <span class="pane-empty-hint">{formatShortcut("K", { mod: true })} to pick a session</span>
        </div>
      </Pane>
    );
  }

  const blocks = projectStream(session);
  const dotState = sessionDotState(session);
  const thinking = session.thinking === "none" ? "off" : (session.thinking || "off");
  const blocking = (session.untrustedMcp || session.pendingPerm || session.pendingAsk) ? (
    <>
      {session.untrustedMcp && <McpBanner key={session.id} sessionId={session.id} />}
      {session.pendingPerm && <PermissionPrompt key={session.id} session={session} />}
      {session.pendingAsk && <AskUserPrompt key={session.id} session={session} />}
    </>
  ) : null;

  return (
    <Pane
      {...commonProps}
      title={sessionTitle(session)}
      state={dotState}
      path={shortPath(session.cwd) || session.cwd || ""}
      model={modelCodename(session.model) || shortModel(session.model) || session.model || ""}
      modelAccent={modelAccent(session.model)}
      thinkingLevel={thinking}
      attention={attention}
      onMaximize={handleMaximize}
      blocking={blocking}
      bodyLive
      composer={<Composer key={session.id} sessionId={session.id} session={session} />}
    >
      <Stream session={session} blocks={blocks} />
    </Pane>
  );
}

// TileNode — recursive render of the tree. `path` accumulates the split path
// used by resizeSplit (setRatioAtPath).
function TileNode({ node, state, path, tileIndexMap }) {
  if (node.type === "tile") {
    return (
      <ConnectedPane
        node={node}
        state={state}
        tileIndex={tileIndexMap.get(node.id) ?? 0}
      />
    );
  }

  const isH = node.direction === "horizontal";
  const [a, b] = node.children;
  const [ra, rb] = node.ratio;

  return (
    <div class={`split ${isH ? "split-h" : "split-v"}`}>
      <div class="split-pane" style={{ flex: ra }}>
        <TileNode node={a} state={state} path={[...path, 0]} tileIndexMap={tileIndexMap} />
      </div>
      <ResizeHandle path={path} direction={node.direction} />
      <div class="split-pane" style={{ flex: rb }}>
        <TileNode node={b} state={state} path={[...path, 1]} tileIndexMap={tileIndexMap} />
      </div>
    </div>
  );
}

export function PaneGrid({ state }) {
  const tileIndexMap = useMemo(() => {
    const ids = allTileIds(state.tileTree);
    const m = new Map();
    ids.forEach((id, i) => m.set(id, i));
    return m;
  }, [state.tileTree]);

  return (
    <div class="pane-grid">
      <TileNode node={state.tileTree} state={state} path={[]} tileIndexMap={tileIndexMap} />
    </div>
  );
}
