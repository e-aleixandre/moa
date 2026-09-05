import { useState, useEffect, useRef, useCallback, useLayoutEffect } from "preact/hooks";
import { createPortal } from "preact/compat";
import { ChatHead } from "../ChatHead/ChatHead.jsx";
import { Stream } from "../Stream/Stream.jsx";
import { LiveDock } from "../LiveDock/LiveDock.jsx";
import { SubagentView } from "../SubagentView/SubagentView.jsx";
import { BashJobView } from "../BashJobView/BashJobView.jsx";
import { Composer } from "../Composer/Composer.jsx";
import { StatusStrip } from "../StatusStrip/StatusStrip.jsx";
import { NowLine } from "../NowLine/NowLine.jsx";
import { RewindTimeline } from "../RewindTimeline/RewindTimeline.jsx";
import { SecretBatch } from "../../components/SecretBatch/SecretBatch.jsx";
import { ModelSelector, PermissionPrompt, AskUserPrompt, McpBanner, UsagePanel, Sheet, ArtifactsEntry } from "../../components/index.js";
import { McpPanel } from "../../components/McpPanel/McpPanel.jsx";
import { LivePreview } from "../../components/LivePreview/LivePreview.jsx";
import { Button, Kbd } from "../../primitives/index.js";
import { updateSession } from "../../data/store.js";
import { useStore } from "../../hooks/useStore.js";
import { projectStream, liveTrayAgents } from "../../data/stream-model.js";
import { focusedSession, focusedSessionId, modelAccent, deriveModelSpecs, matchSelectedModel, thinkingPositionFor } from "../../data/selectors.js";
import { navigate } from "../../data/router.js";
import { openPalette } from "../../data/palette.js";
import { registerOverlay } from "../../data/overlays.js";
import { shortModel, shortPath, modelCodename, sessionTitle } from "../../data/util/format.js";
import { fmtCost } from "../../data/util/usage-pills.js";
import { formatShortcut } from "../../data/util/shortcut.js";
import { Plus } from "lucide-preact";
import { api } from "../../data/api.js";
import { addToast } from "../../data/notifications.js";
import { configureSession, openPersistedSubagent, openBashJob, rewindToMessage, setSessionFast } from "../../data/session-actions.js";
import { positionModelPopover } from "../PaneGrid/model-popover-position.js";
import "./ConversationScreen.css";

// ConversationScreen — the desktop conversation column. Spine lives in
// DesktopShell; this container subscribes to the store, derives the focused
// session, and passes props down to ChatHead / Stream / StatusStrip.
//
// Three states: LOADING (sessions not fetched yet), EMPTY (no focused session),
// and a normal shown session.

function fmtSpend(costUSD) {
  if (!costUSD || costUSD <= 0) return undefined;
  return fmtCost(costUSD);
}

export function ConversationScreen() {
  const session = useStore(focusedSession);
  const activeId = useStore(focusedSessionId);
  const loaded = useStore((s) => s.sessionsLoaded);
  const usage = useStore((s) => s.usage);

  // --- Live Dock (SUBAGENTS-PERSISTENT-SPEC) ---
  // The dock is the permanent home for live ASYNC work (async subagents + bash)
  // above the composer ("async in the dock, sync inline"). Sync subagents stay
  // inline in the delegation block instead.
  const liveAgents = session ? liveTrayAgents(session) : [];

  // --- Model selector popover (StatusStrip's ModelPill) ---
  const [modelOpen, setModelOpen] = useState(false);
  const [models, setModels] = useState(null); // null = not fetched yet
  const modelAnchorRef = useRef(null);
  const modelPopoverRef = useRef(null);
  const [modelPopoverPosition, setModelPopoverPosition] = useState(null);
  useEffect(() => {
    if (!modelOpen || models) return;
    api("GET", "/api/models").then(setModels).catch(() => setModels([]));
  }, [modelOpen, models]);
  useEffect(() => {
    if (!modelOpen) return;
    const unregister = registerOverlay("conv-model-popover");
    const onDocDown = (e) => {
      const target = e.target;
      if (modelAnchorRef.current?.contains(target) || modelPopoverRef.current?.contains(target)) return;
      setModelOpen(false);
    };
    const onKeyDown = (e) => { if (e.key === "Escape") setModelOpen(false); };
    document.addEventListener("mousedown", onDocDown);
    document.addEventListener("keydown", onKeyDown);
    return () => {
      unregister();
      document.removeEventListener("mousedown", onDocDown);
      document.removeEventListener("keydown", onKeyDown);
    };
  }, [modelOpen]);

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
      setModelPopoverPosition(null);
      return undefined;
    }
    placeModelPopover();
    window.addEventListener("resize", placeModelPopover);
    window.addEventListener("scroll", placeModelPopover, true);
    const observer = typeof ResizeObserver === "undefined"
      ? null
      : new ResizeObserver(placeModelPopover);
    if (observer) {
      if (modelAnchorRef.current) observer.observe(modelAnchorRef.current);
      if (modelPopoverRef.current) observer.observe(modelPopoverRef.current);
    }
    return () => {
      window.removeEventListener("resize", placeModelPopover);
      window.removeEventListener("scroll", placeModelPopover, true);
      observer?.disconnect();
    };
  }, [modelOpen, placeModelPopover]);

  // Close popovers when the focused session changes.
  useEffect(() => {
    setModelOpen(false);
  }, [activeId]);

  // --- Rewind timeline sheet ---
  const [rewindOpen, setRewindOpen] = useState(false);
  const [secretAliases, setSecretAliases] = useState(null);
  useEffect(() => { setRewindOpen(false); }, [activeId]);
  useEffect(() => { setSecretAliases(null); }, [activeId]);

  // --- Usage panel popover (StatusStrip's cost segment) — level 2 telemetry
  // (TELEMETRY-SETTINGS-REDESIGN §2). Anchored to the strip, not the head, but
  // reuses the exact same click-outside + Escape wiring as the head popovers.
  const [usageOpen, setUsageOpen] = useState(false);
  const usageAnchorRef = useRef(null);
  useEffect(() => { setUsageOpen(false); }, [activeId]);
  useEffect(() => {
    if (!usageOpen) return;
    const unregister = registerOverlay("conv-usage-popover");
    const onDocDown = (e) => {
      if (usageAnchorRef.current && !usageAnchorRef.current.contains(e.target)) setUsageOpen(false);
    };
    const onKeyDown = (e) => { if (e.key === "Escape") setUsageOpen(false); };
    document.addEventListener("mousedown", onDocDown);
    document.addEventListener("keydown", onKeyDown);
    return () => {
      unregister();
      document.removeEventListener("mousedown", onDocDown);
      document.removeEventListener("keydown", onKeyDown);
    };
  }, [usageOpen]);

  // --- MCP panel popover (StatusStrip's MCP segment) — per-session server
  // health + restart. Same anchor/click-outside/Escape wiring as the usage
  // popover; its own anchor so the two don't fight over one ref.
  const [mcpOpen, setMcpOpen] = useState(false);
  useEffect(() => { setMcpOpen(false); }, [activeId]);
  useEffect(() => {
    if (!mcpOpen) return;
    const unregister = registerOverlay("conv-mcp-popover");
    const onDocDown = (e) => {
      if (usageAnchorRef.current && !usageAnchorRef.current.contains(e.target)) setMcpOpen(false);
    };
    const onKeyDown = (e) => { if (e.key === "Escape") setMcpOpen(false); };
    document.addEventListener("mousedown", onDocDown);
    document.addEventListener("keydown", onKeyDown);
    return () => {
      unregister();
      document.removeEventListener("mousedown", onDocDown);
      document.removeEventListener("keydown", onKeyDown);
    };
  }, [mcpOpen]);

  let body;
  if (!loaded) {
    body = <div class="conversation-placeholder">Loading sessions…</div>;
  } else if (!session) {
    body = (
      <div class="conversation-empty">
        <span class="conversation-empty-glyph" aria-hidden="true">m</span>
        <p class="conversation-empty-title">No session open</p>
        <p class="conversation-empty-hint">
          Pick a session from the sidebar, or press{" "}
          <Kbd>{formatShortcut("K", { mod: true })}</Kbd> to jump.
        </p>
        <div class="conversation-empty-actions">
          <Button variant="solid" size="md" onClick={() => openPalette("create")}>
            <Plus size={14} aria-hidden="true" /> New session
          </Button>
        </div>
      </div>
    );
  } else {
    const blocks = projectStream(session);
    const specs = deriveModelSpecs(models);
    const selectedModel = matchSelectedModel(specs, session.model);
    const thinking = session.thinking === "none" ? "off" : (session.thinking || "off");
    const settingsBusy = session.state === "running" || session.state === "permission";
    // When a subagent is being viewed, the SubagentView takes over the main
    // column (in place of the parent stream/composer/status). Its jobId must
    // still exist in the session (the view itself rebounds to null via onBack if
    // it was pruned).
    const viewingSub = session.viewingSubagent;
    // Same slot for a background bash job's read-only view (the dock's other
    // openable row). The two are mutually exclusive by construction (opening
    // one clears the other), and the subagent wins any residual tie.
    const viewingBash = !viewingSub && session.viewingBashJob;

    // Back from bash/subagent: clear the detail view and, when the detail was
    // opened from the pane grid (detailReturnView === "grid"), restore the grid
    // so the user does not lose the multi-pane layout (TOC-4).
    const leaveDetail = (patch) => {
      const returnView = session.detailReturnView;
      updateSession(session.id, { ...patch, detailReturnView: null });
      if (returnView === "grid") navigate("grid");
    };

    const modelPopover = modelOpen && typeof document !== "undefined" && document.body && createPortal(
      <div
        class="head-popover conversation-model-popover"
        ref={modelPopoverRef}
        style={{
          left: modelPopoverPosition?.left,
          top: modelPopoverPosition?.top,
          visibility: modelPopoverPosition ? undefined : "hidden",
        }}
      >
        <ModelSelector
          models={specs}
          selected={selectedModel}
          thinking={thinking}
          sessionModel={session.model || ""}
          sessionProvider={session.provider}
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
      </div>,
      document.body,
    );

    body = (
      <>
        <ChatHead
          title={sessionTitle(session)}
          path={shortPath(session.cwd) || session.cwd || ""}
          onGridToggle={() => navigate("grid")}
          previewOpen={!!session.previewOpen}
          onPreviewToggle={() => updateSession(session.id, { previewOpen: !session.previewOpen })}
          headExtra={<ArtifactsEntry sessionId={session.id} />}
        />
        {viewingSub ? (
          <SubagentView
            key={viewingSub}
            session={session}
            jobId={viewingSub}
            onBack={() => leaveDetail({ viewingSubagent: null })}
          />
        ) : viewingBash ? (
          <BashJobView
            key={viewingBash}
            session={session}
            jobId={viewingBash}
            onBack={() => leaveDetail({ viewingBashJob: null })}
          />
        ) : (
          <>
            <Stream
              session={session}
              blocks={blocks}
              rewind={{
                to: (msgId) => rewindToMessage(session.id, msgId),
                openTimeline: () => setRewindOpen(true),
                disabled: settingsBusy,
              }}
              onOpenSubagent={(id) => openPersistedSubagent(session.id, id)}
              tail={session.pendingAsk ? <AskUserPrompt key={session.id} session={session} /> : null}
            />
            {(session.untrustedMcp || session.pendingPerm) && (
              <div class="conversation-blocking">
                {session.untrustedMcp && <McpBanner key={session.id} sessionId={session.id} />}
                {session.pendingPerm && <PermissionPrompt key={session.id} session={session} />}
              </div>
            )}
            {/* Live Dock — the permanent home for live async work ("async in
                the dock, sync inline"). Shown whenever there's async work; its
                open/closed state persists per session (session.dockOpen). */}
            {liveAgents.length > 0 && (
              <LiveDock
                agents={liveAgents}
                open={!!session.dockOpen}
                onToggle={(next) => updateSession(session.id, { dockOpen: next })}
                onOpen={(id, kind) => (kind === "bash"
                  ? openBashJob(session.id, id)
                  : openPersistedSubagent(session.id, id))}
              />
            )}
            {/* The activity now-line sits ABOVE the input, as on mobile: what
                is happening NOW belongs next to where you'd interrupt it, while
                the strip below keeps the standing telemetry (context, cost,
                permissions, MCP, tokens). flex:none, so it pushes the stream up
                instead of overlaying the composer. */}
            <NowLine session={session} />
            <Composer key={session.id} sessionId={session.id} session={session} onSecret={setSecretAliases} />
            <div class="status-strip-anchor" ref={usageAnchorRef}>
              <StatusStrip
                ctxPercent={session.contextPercent}
                tokensUp={session.runTokensUp}
                tokensDown={session.runTokensDown}
                spend={fmtSpend(session.costUSD)}
                session={session}
                usage={usage}
                onOpenUsage={() => setUsageOpen((v) => !v)}
                onOpenMcp={() => setMcpOpen((v) => !v)}
                onPermChange={(mode) => configureSession(session.id, { permissionMode: mode })}
                permBusy={settingsBusy}
                showTokens={true}
                modelName={modelCodename(session.model) || shortModel(session.model) || session.model || ""}
                modelAccent={modelAccent(session.model)}
                thinking={thinking}
                thinkingPosition={thinkingPositionFor(thinking, specs.find((spec) => spec.id === selectedModel), session.provider)}
                onModel={() => setModelOpen((v) => !v)}
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
              {mcpOpen && (
                <div class="status-strip-usage-popover status-strip-mcp-popover">
                  <McpPanel sessionId={session.id} mcpTick={session.mcpTick} />
                </div>
              )}
            </div>
          </>
        )}
      </>
    );
  }

  return (
    <>
      <main class="conversation-main">
        {body}
        {session && (
          <LivePreview
            sessionId={session.id}
            open={!!session.previewOpen}
            inline
            onClose={() => updateSession(session.id, { previewOpen: false })}
          />
        )}
      </main>
      {session && (
        <RewindTimeline
          open={rewindOpen}
          onClose={() => setRewindOpen(false)}
          sessionId={session.id}
        />
      )}
      {session && (
        <Sheet open={secretAliases !== null} onClose={() => setSecretAliases(null)} title="Send secrets">
          <SecretBatch
            open={secretAliases !== null}
            sessionId={session.id}
            aliases={secretAliases || []}
            onClose={() => setSecretAliases(null)}
          />
        </Sheet>
      )}
    </>
  );
}
