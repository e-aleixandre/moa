import { useCallback, useMemo, useRef, useState, useEffect, useLayoutEffect } from "preact/hooks";
import { createPortal } from "preact/compat";
import { MessageSquarePlus } from "lucide-preact";
import { Pane } from "../Pane/Pane.jsx";
import { Stream } from "../Stream/Stream.jsx";
import { Composer } from "../Composer/Composer.jsx";
import { StatusStrip } from "../StatusStrip/StatusStrip.jsx";
import { LiveBar } from "../LiveBar/LiveBar.jsx";
import {
  McpBanner, PermissionPrompt, AskUserPrompt, UsagePanel, ModelSelector, ArtifactsPaneButton,
} from "../../components/index.js";
import { PickerPopover } from "../../components/ModelSelector/ModelSelector.jsx";
import { usePermissionMenu } from "../../components/PermissionControl/PermissionControl.jsx";
import { LivePreview } from "../../components/LivePreview/LivePreview.jsx";
import { Sheet } from "../../components/Sheet/Sheet.jsx";
import { SecretBatch } from "../../components/SecretBatch/SecretBatch.jsx";
import { snapToRatio } from "../../data/snap.js";
import { formatShortcut } from "../../data/util/shortcut.js";
import {
  resizeSplit, assignToTile, swapTiles, splitTile, closeTile, focusTile,
} from "../../data/tile-actions.js";
import { navigate } from "../../data/router.js";
import { allTileIds } from "../../data/tileTree.js";
import { getTileCount, updateSession } from "../../data/store.js";
import { useStore } from "../../hooks/useStore.js";
import { usePresence } from "../../hooks/usePresence.js";
import { projectStream, liveTrayAgents } from "../../data/stream-model.js";
import { openPersistedSubagent, openBashJob, configureSession, setSessionFast, stopRun } from "../../data/session-actions.js";
import { matchSelectedModel } from "../../data/selectors.js";
import { catalogThinkingPosition, ensureModelCatalog, modelCatalog } from "../../data/model-catalog.js";
import { shortModel, shortPath, sessionDisplayDotState, modelCodename, sessionTitle } from "../../data/util/format.js";
import { fmtCost } from "../../data/util/usage-pills.js";
import { useTouchDrag, registerDropTarget } from "../../hooks/useTouchDrag.js";
import { addToast } from "../../data/notifications.js";
import { registerOverlay } from "../../data/overlays.js";
import { setPopoverOpenFromClick } from "../../data/popover-click.js";
import { openSessionPanel } from "../../data/session-panel.js";
import { positionModelPopover } from "./model-popover-position.js";
import "./PaneGrid.css";

// PaneGrid. Renders the REAL binary split tree (state.tileTree) recursively:
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
// Pane's optional connected props to the tile actions and mounts the live Stream +
// Composer (+ blocking) when a session is assigned.
export function ConnectedPane({ node, tileIndex, onSecret }) {
  const tileId = node.id;
  const sessionId = node.sessionId || null;
  const session = useStore((s) => (sessionId ? s.sessions[sessionId] : null));
  const focused = useStore((s) => s.focusedTile === tileId);
  const usage = useStore((s) => s.usage);
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
  // Interactive chrome (status strip, model pill, popovers, tools) must not
  // steal focus into the composer — otherwise usage/perm/mcp/model clicks feel
  // dead because the caret jumps to the input.
  const handleFocus = useCallback((e) => {
    const t = e.target;
    if (t && t.closest && t.closest(
      'input, textarea, [contenteditable="true"], .ask-user-card, .zl-composer, .composer, '
      + '.zl-status, .status-strip, .status-strip-anchor, '
      + '.zl-pane-head, .p-tools, .head-popover, .status-strip-usage-popover, button, a, [role="menu"]'
    )) {
      focusTile(tileId, { focusInput: false });
      return;
    }
    focusTile(tileId, { respectSelection: true });
  }, [tileId]);

  // --- Maximize → back to conversation view with this session focused ---
  const handleMaximize = useCallback(() => {
    if (!node.sessionId) return;
    // Keep the session in the focused tile, then leave the grid in place: the
    // router flips the view in place (no reload) and the conversation screen
    // renders the focused tile's session. navigate({session}) focuses it first.
    assignToTile(tileId, node.sessionId);
    navigate(null, { session: node.sessionId });
  }, [tileId, node.sessionId]);

  // --- Pane telemetry / model popovers (parity with ConversationScreen) ---
  // Without these handlers the StatusStrip is read-only and model is decorative:
  // clicks look broken (TOC-3). Popovers are local to this pane so multi-pane
  // grids don't share one global open state.
  const [usageOpen, setUsageOpen] = useState(false);
  const [modelOpen, setModelOpen] = useState(false);
  const catalog = useStore(modelCatalog);
  const stripAnchorRef = useRef(null);
  const modelAnchorRef = useRef(null);
  const modelPopoverRef = useRef(null);
  const [modelPopoverPosition, setModelPopoverPosition] = useState(null);

  const settingsBusy = !!session && (session.state === "running" || session.state === "permission");
  const permMenu = usePermissionMenu({
    mode: session?.permissionMode || "yolo",
    disabled: settingsBusy,
    onChange: (mode) => {
      if (!session) return;
      configureSession(session.id, { permissionMode: mode });
    },
  });

  useEffect(() => {
    setUsageOpen(false);
    setModelOpen(false);
    permMenu.close();
  }, [node.sessionId]);

  useEffect(() => {
    if (modelOpen) ensureModelCatalog();
  }, [modelOpen]);

  useEffect(() => {
    if (!modelOpen) return undefined;
    const unregister = registerOverlay(`pane-model-popover-${tileId}`);
    return () => unregister();
  }, [modelOpen, tileId]);

  const placeModelPopover = useCallback(() => {
    const anchor = modelAnchorRef.current?.getBoundingClientRect();
    const popover = modelPopoverRef.current?.getBoundingClientRect();
    if (!anchor || !popover) return;
    setModelPopoverPosition(positionModelPopover(anchor, popover, {
      width: window.innerWidth,
      height: window.innerHeight,
    }));
  }, []);

  useLayoutEffect(() => {
    if (!modelOpen) {
      // Not cleared here: see ConversationScreen. Dropping the position on
      // close hides the popover on the first frame of its exit.
      return undefined;
    }
    placeModelPopover();
    window.addEventListener("resize", placeModelPopover);
    // Capture nested pane scrolling as well as document scrolling.
    window.addEventListener("scroll", placeModelPopover, true);
    const observer = typeof ResizeObserver === "undefined"
      ? null
      : new ResizeObserver(placeModelPopover);
    if (observer && modelPopoverRef.current) observer.observe(modelPopoverRef.current);
    return () => {
      window.removeEventListener("resize", placeModelPopover);
      window.removeEventListener("scroll", placeModelPopover, true);
      observer?.disconnect();
    };
  }, [modelOpen, placeModelPopover]);

  useEffect(() => {
    if (!usageOpen && !modelOpen) return undefined;
    const onDocDown = (e) => {
      const t = e.target;
      if (stripAnchorRef.current?.contains(t)) return;
      if (modelAnchorRef.current?.contains(t)) return;
      if (modelPopoverRef.current?.contains(t)) return;
      setUsageOpen(false);
      setModelOpen(false);
    };
    const onKeyDown = (e) => {
      if (e.key === "Escape") {
        setUsageOpen(false);
        setModelOpen(false);
      }
    };
    document.addEventListener("mousedown", onDocDown);
    document.addEventListener("keydown", onKeyDown);
    return () => {
      document.removeEventListener("mousedown", onDocDown);
      document.removeEventListener("keydown", onKeyDown);
    };
  }, [usageOpen, modelOpen]);

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
        <div class="zl-pane-empty">
          <MessageSquarePlus aria-hidden="true" />
          <span class="zl-pane-empty-title">Drag a session here</span>
          <span class="zl-pane-empty-hint">{formatShortcut("K", { mod: true })} to pick a session</span>
        </div>
      </Pane>
    );
  }

  const blocks = projectStream(session);
  const liveAgents = liveTrayAgents(session);
  const dotState = sessionDisplayDotState(session);
  const thinking = session.thinking === "none" ? "off" : (session.thinking || "off");
  const blocking = (session.untrustedMcp || session.pendingPerm || session.pendingAsk) ? (
    <>
      {session.untrustedMcp && <McpBanner key={session.id} sessionId={session.id} />}
      {session.pendingPerm && <PermissionPrompt key={session.id} session={session} />}
      {session.pendingAsk && <AskUserPrompt key={session.id} session={session} />}
    </>
  ) : null;

  const specs = catalog.entries || [];
  const selectedModel = matchSelectedModel(specs, session.model);
  const modelPopoverPresence = usePresence(modelOpen);
  useEffect(() => {
    if (!modelPopoverPresence.mounted) setModelPopoverPosition(null);
  }, [modelPopoverPresence.mounted]);
  const modelPopover = modelPopoverPresence.mounted && typeof document !== "undefined" && document.body && createPortal(
    <PickerPopover
      kind="model"
      class="is-fixed"
      models={specs}
      popoverRef={modelPopoverRef}
      style={{
        left: modelPopoverPosition?.left,
        top: modelPopoverPosition?.top,
        visibility: modelPopoverPosition ? undefined : "hidden",
      }}
      onClose={() => setModelOpen(false)}
      leaving={modelPopoverPresence.leaving}
    >
      {(v) => (
        <ModelSelector
          models={specs}
          selected={selectedModel}
          thinking={thinking}
          sessionModel={session.model || ""}
          sessionProvider={session.provider}
          view={v.view}
          setView={v.setView}
          onSelect={(spec) => {
            configureSession(session.id, { model: spec })
              .then(() => setModelOpen(false))
              .catch((error) => addToast({
                title: "Could not change model",
                detail: error.message,
                type: "error",
              }));
          }}
          onThinkingChange={(value) => configureSession(session.id, { thinking: value })}
          fast={!!session.fast}
          fastSupported={!!session.fastSupported}
          fastNote={session.fastNote || ""}
          onFastChange={(value) => {
            setSessionFast(session.id, value).catch((error) => addToast({
              title: "Could not change fast mode",
              detail: String(error.message || error),
              type: "error",
            }));
          }}
        />
      )}
    </PickerPopover>,
    document.body,
  );

  return (
    <Pane
      {...commonProps}
      title={sessionTitle(session)}
      state={dotState}
      path={shortPath(session.cwd) || session.cwd || ""}
      attention={attention}
      previewOpen={!!session.previewOpen}
      onPreviewToggle={(e) => { e.stopPropagation(); updateSession(session.id, { previewOpen: !session.previewOpen }); }}
      headExtra={<ArtifactsPaneButton sessionId={session.id} />}
      onMaximize={handleMaximize}
      blocking={blocking}
      composer={(
        <Composer key={session.id} sessionId={session.id} session={session} compact onSecret={(aliases) => onSecret(session.id, aliases)} />
      )}
      dock={(
        <LiveBar
          key={session.id}
          session={session}
          agents={liveAgents}
          dense
          open={!!session.dockOpen}
          onToggle={(next) => updateSession(session.id, { dockOpen: next })}
          onOpen={async (jobId, kind) => {
            // Detail views need room; open them in single conversation but
            // remember the grid so Back restores the layout (TOC-4).
            if (kind === "bash") openBashJob(session.id, jobId, { returnView: "grid" });
            else await openPersistedSubagent(session.id, jobId, { returnView: "grid" });
            navigate(null, { session: session.id });
          }}
          onStop={() => stopRun(session.id).catch(() => {})}
        />
      )}
      status={(
        <div class="status-strip-anchor pane-status-anchor" ref={stripAnchorRef}>
          <StatusStrip
            compact
            ctxPercent={session.contextPercent}
            tokensUp={session.runTokensUp}
            tokensDown={session.runTokensDown}
            spend={fmtCost(session.costUSD)}
            session={session}
            usage={usage}
            onOpenUsage={(event) => {
              setModelOpen(false);
              setPopoverOpenFromClick(setUsageOpen, event);
            }}
            onOpenMcp={() => {
              setUsageOpen(false);
              setModelOpen(false);
              navigate(null, { session: session.id });
              openSessionPanel(session.id, "mcp");
            }}
            mcpOpen={false}
            onPerm={permMenu.toggle}
            permOpen={permMenu.open}
            permAnchorRef={permMenu.anchorRef}
            permPopover={permMenu.menu}
            permBusy={settingsBusy}
            showTokens
            modelName={modelCodename(session.model) || shortModel(session.model) || session.model || ""}
            thinking={thinking}
            thinkingPosition={catalogThinkingPosition(catalog, {
              model: session.model,
              provider: session.provider,
              thinking,
            })}
            onModel={(event) => {
              setUsageOpen(false);
              setPopoverOpenFromClick(setModelOpen, event);
            }}
            modelOpen={modelOpen}
            modelPopover={modelPopover}
            modelAnchorRef={modelAnchorRef}
          />
          {usageOpen && (
            <div class="status-strip-usage-popover">
              <UsagePanel
                session={session}
                usage={usage}
                ctxPercent={session.contextPercent}
                costUSD={session.costUSD}
              />
            </div>
          )}
        </div>
      )}
      overlay={(
        <LivePreview
          sessionId={session.id}
          open={!!session.previewOpen}
          inline
          onClose={() => updateSession(session.id, { previewOpen: false })}
        />
      )}
    >
      <Stream
        session={session}
        blocks={blocks}
      />
    </Pane>
  );
}

// TileNode — recursive render of the tree. `path` accumulates the split path
// used by resizeSplit (setRatioAtPath).
function TileNode({ node, path, tileIndexMap, onSecret }) {
  if (node.type === "tile") {
    return (
      <ConnectedPane
        node={node}
        tileIndex={tileIndexMap.get(node.id) ?? 0}
        onSecret={onSecret}
      />
    );
  }

  const isH = node.direction === "horizontal";
  const [a, b] = node.children;
  const [ra, rb] = node.ratio;

  return (
    <div class={`split ${isH ? "split-h" : "split-v"}`}>
      <div class="split-pane" style={{ flex: ra }}>
        <TileNode node={a} path={[...path, 0]} tileIndexMap={tileIndexMap} onSecret={onSecret} />
      </div>
      <ResizeHandle path={path} direction={node.direction} />
      <div class="split-pane" style={{ flex: rb }}>
        <TileNode node={b} path={[...path, 1]} tileIndexMap={tileIndexMap} onSecret={onSecret} />
      </div>
    </div>
  );
}

export function PaneGrid({ children }) {
  const tileTree = useStore((s) => s.tileTree);
  const [secretBatch, setSecretBatch] = useState(null);
  const tileIndexMap = useMemo(() => {
    const ids = allTileIds(tileTree);
    const m = new Map();
    ids.forEach((id, i) => m.set(id, i));
    return m;
  }, [tileTree]);

  // The catalogue's 2+1 fixture arrives as children: same panes container,
  // no store tree. Production renders the binary split into the same wrap.
  if (children) {
    return <div class="zl-grid-panes">{children}</div>;
  }

  return (
    <>
      <div class="zl-grid-panes">
        <TileNode
          node={tileTree}
          path={[]}
          tileIndexMap={tileIndexMap}
          onSecret={(sessionId, aliases) => setSecretBatch({ sessionId, aliases })}
        />
      </div>
      <Sheet open={secretBatch !== null} onClose={() => setSecretBatch(null)} title="Send secrets">
        <SecretBatch
          open={secretBatch !== null}
          sessionId={secretBatch?.sessionId || ""}
          aliases={secretBatch?.aliases || []}
          onClose={() => setSecretBatch(null)}
        />
      </Sheet>
    </>
  );
}
