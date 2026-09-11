import { useState, useEffect } from "preact/hooks";
import { ModelSelector } from "../../../components/index.js";
import { PermissionOptions } from "../../../components/PermissionControl/PermissionControl.jsx";
import { statusStripModel } from "../../../data/util/status-strip-model.js";
import { fmtCost } from "../../../data/util/usage-pills.js";
import { matchSelectedModel, modelAccent } from "../../../data/selectors.js";
import { catalogThinkingPosition, ensureModelCatalog, modelCatalog } from "../../../data/model-catalog.js";
import { useStore } from "../../../hooks/useStore.js";
import { configureSession, setSessionFast } from "../../../data/session-actions.js";
import { toggleSessionPanel } from "../../../data/session-panel.js";
import { addToast } from "../../../data/notifications.js";
import { modelCodename, shortModel } from "../../../data/util/format.js";
import { MobileSheet } from "../MobileSheet/MobileSheet.jsx";
import { StatusStrip } from "../../StatusStrip/StatusStrip.jsx";
import { McpPanel } from "../../../components/McpPanel/McpPanel.jsx";

// MobileStatusLine — the phone's host for the status line. The line's FACE is
// StatusStrip, the same component as desktop and grid; what lives here is what
// each of its doors OPENS at this density.
//
// Every door on the line opens over the line, as a bottom sheet (MobileSheet)
// — never the centered generic <Sheet> modal. They are the phone's form of the
// desktop's popovers, and they hold the settings for the NEXT turn:
//
//   • model — "Model & thinking": the real ModelSelector, and nothing else.
//   • permission — the glanceable safety colour AND the door. ONE tap reveals
//     the complete YOLO/AUTO/ASK choice, from PermissionControl's own rows, so
//     the two densities cannot drift apart.
//   • mcp — the per-session server health, from the shared McpPanel.
//
// The gauges are the exception, and the only door here that does NOT open over
// the line: the ring opens the session PANEL on its Usage page, the same
// component and controller the desktop dossier uses (data/session-panel.js),
// hosted by MobileConversationScreen. It used to open a sheet written here,
// which re-laid the panel's Usage page in a second vocabulary (.msl-usage) —
// the auto-compaction slider was the only thing it owned, and that moved onto
// the page itself (components/SessionPanel/UsagePage.jsx).
//
// Sessions is deliberately NOT here: the door to the other sessions is the
// header's left capsule (MobileChrome), which also carries the cross-session
// attention dot. One door per destination.

export function MobileStatusLine({ session, usage }) {
  const [sessionOpen, setSessionOpen] = useState(false);
  const [permsOpen, setPermsOpen] = useState(false);
  const [mcpOpen, setMcpOpen] = useState(false);
  const catalog = useStore(modelCatalog);

  const sessionId = session ? session.id : null;
  useEffect(() => {
    setSessionOpen(false);
    setPermsOpen(false);
    setMcpOpen(false);
  }, [sessionId]);

  // Opening the sheet is a natural moment to retry a catalog that never
  // arrived; the bootstrap already asked once.
  useEffect(() => {
    if (sessionOpen) ensureModelCatalog();
  }, [sessionOpen]);

  const hasSession = !!session;
  const ctx = hasSession ? session.contextPercent : undefined;
  const hasCtx = typeof ctx === "number" && ctx >= 0;
  const model = statusStripModel(session, usage);
  const spend = hasSession && session.costUSD > 0 ? fmtCost(session.costUSD) : undefined;
  const busy = hasSession && (session.state === "running" || session.state === "permission");
  // Per-run token heartbeat — shown only once the run has actually moved any,
  // so an idle session's line stays quiet rather than reading a hollow "↑0 ·↓0".
  const tokensUp = hasSession ? session.runTokensUp : undefined;
  const tokensDown = hasSession ? session.runTokensDown : undefined;
  const hasTokens = (tokensUp || 0) > 0 || (tokensDown || 0) > 0;

  const specs = catalog.entries || [];
  const thinking = hasSession
    ? session.thinking === "none"
      ? "off"
      : session.thinking || "off"
    : "off";
  const modelName = hasSession
    ? modelCodename(session.model) || shortModel(session.model) || session.model || ""
    : "";
  const permMode = model.perm.mode;

  const changePerm = (value) => {
    if (value !== permMode) configureSession(session.id, { permissionMode: value });
    setPermsOpen(false);
  };

  return (
    <StatusStrip
      compact
      ctxPercent={hasCtx ? ctx : undefined}
      tokensUp={hasTokens ? tokensUp : 0}
      tokensDown={hasTokens ? tokensDown : 0}
      spend={spend}
      session={session}
      usage={usage}
      onOpenUsage={hasSession ? () => toggleSessionPanel(session.id, "usage") : undefined}
      onOpenMcp={hasSession ? () => setMcpOpen(true) : undefined}
      mcpOpen={mcpOpen}
      onPerm={hasSession ? () => setPermsOpen(true) : undefined}
      permOpen={permsOpen}
      showTokens
      modelName={modelName}
      modelAccent={hasSession ? modelAccent(session.model) : "lavender"}
      thinking={thinking}
      thinkingPosition={catalogThinkingPosition(catalog, {
        model: session?.model,
        provider: session?.provider,
        thinking,
      })}
      onModel={hasSession ? () => setSessionOpen(true) : undefined}
      modelOpen={sessionOpen}
    >
      {hasSession && (
        <MobileSheet
          open={sessionOpen}
          onClose={() => setSessionOpen(false)}
          // No `scope` here: the path was this sheet's header back when it was
          // "This session" and the question was where the session runs. On a
          // sheet that only picks a model it is one more piece of session info
          // nobody asked for — and long enough to wrap the title onto two
          // lines. The cwd lives with the sessions, in the drawer.
          title="Model & thinking"
        >
          <ModelSelector
            models={specs}
            selected={matchSelectedModel(specs, session.model)}
            thinking={thinking}
            embedded
            sessionModel={session.model || ""}
            sessionProvider={session.provider}
            onSelect={(spec) => {
              configureSession(session.id, { model: spec })
                .then(() => setSessionOpen(false))
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

        </MobileSheet>
      )}

      {hasSession && (
        <MobileSheet
          open={permsOpen}
          onClose={() => setPermsOpen(false)}
          title="Permissions"
          scope="this session"
        >
          <div class="perm-sheet-list" role="menu" aria-label="Permission mode">
            <PermissionOptions
              mode={permMode}
              onPick={changePerm}
              isDisabled={(_value, on) => busy && !on}
            />
          </div>
        </MobileSheet>
      )}

      {hasSession && session.mcp && session.mcp.total > 0 && (
        <MobileSheet
          open={mcpOpen}
          onClose={() => setMcpOpen(false)}
          title="MCP servers"
        >
          <McpPanel sessionId={session.id} mcpTick={session.mcpTick} variant="sheet" />
        </MobileSheet>
      )}
    </StatusStrip>
  );
}
