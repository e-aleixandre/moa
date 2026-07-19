import { useState, useEffect, useCallback } from "preact/hooks";
import { GitBranch, Rewind as RewindIcon } from "lucide-preact";
import { Sheet } from "../../components/Sheet/Sheet.jsx";
import { Spinner } from "../../primitives/index.js";
import { fetchBranchPoints, branchTo } from "../../data/session-actions.js";
import { registerOverlay } from "../../data/overlays.js";
import {
  normalizeBranchPoints, rewindSummary, currentPathTipId, isJumpable,
  formatRelativeTime,
} from "../../data/rewind-model.js";
import "./RewindTimeline.css";

// RewindTimeline — non-destructive time travel. Opens as a Sheet with a
// vertical list of branch points (both user AND assistant turns, per
// INC-29); jumping to one calls branchTo, which starts a NEW branch on the
// server — nothing is ever deleted, and the WebSocket reload (ws-handlers.js
// 'branch' command) takes care of refreshing the message list, so this
// component only needs to close itself after a successful jump.
//
// Shared between desktop (ChatHead's Rewind button) and mobile (MobileHeader's
// rewind control) — same component, only the Sheet's own responsive layout
// changes density.
export function RewindTimeline({ open, onClose, sessionId, disabled }) {
  const [status, setStatus] = useState("idle"); // idle | loading | ready | error
  const [points, setPoints] = useState([]);
  const [jumpingId, setJumpingId] = useState(null);

  useEffect(() => {
    if (!open) return;
    const unregister = registerOverlay("rewind-timeline");
    return unregister;
  }, [open]);

  useEffect(() => {
    if (!open || !sessionId) return;
    setStatus("loading");
    let cancelled = false;
    fetchBranchPoints(sessionId)
      .then((res) => {
        if (cancelled) return;
        setPoints(normalizeBranchPoints(res));
        setStatus("ready");
      })
      .catch(() => {
        if (cancelled) return;
        setStatus("error");
      });
    return () => { cancelled = true; };
  }, [open, sessionId]);

  // Reset per-open state so a stale list/jump spinner doesn't flash on reopen.
  useEffect(() => {
    if (!open) {
      setPoints([]);
      setStatus("idle");
      setJumpingId(null);
    }
  }, [open]);

  const tipId = currentPathTipId(points);

  const onJump = useCallback((point) => {
    if (!isJumpable(point, tipId) || jumpingId) return;
    setJumpingId(point.entryId);
    branchTo(sessionId, point.entryId)
      .then(() => onClose?.())
      .catch(() => setJumpingId(null));
  }, [sessionId, tipId, jumpingId, onClose]);

  const { pointCount, branchCount } = rewindSummary(points);
  const countLabel = status === "ready" ? `${pointCount} points · ${branchCount} branches` : "";

  return (
    <Sheet
      open={open}
      onClose={onClose}
      ariaLabel="Rewind"
      title={
        <>
          Rewind
          {countLabel && <span class="rw-count">{countLabel}</span>}
        </>
      }
    >
      <div class="rewind-timeline">
        <div class="rw-legend">
          <span class="rw-legend-item"><i class="rw-dot rw-dot-user" /> your messages</span>
          <span class="rw-legend-item"><i class="rw-dot rw-dot-assistant" /> assistant turns</span>
        </div>

        {status === "loading" && (
          <div class="rw-state">
            <Spinner size={16} />
            <span>Loading branch points…</span>
          </div>
        )}

        {status === "error" && (
          <div class="rw-state rw-state-error">
            <span>Couldn't load the rewind timeline.</span>
          </div>
        )}

        {status === "ready" && points.length === 0 && (
          <div class="rw-state">
            <span>No branch points yet.</span>
          </div>
        )}

        {status === "ready" && points.length > 0 && (
          <ul class="rw-track" role="list">
            {points.map((point) => {
              const isTip = point.entryId === tipId;
              const jumpable = isJumpable(point, tipId) && disabled !== true;
              const jumping = jumpingId === point.entryId;
              return (
                <li
                  key={point.entryId}
                  class={`rw-item rw-role-${point.role}${isTip ? " rw-item-here" : ""}${point.branchCount > 0 ? " rw-item-branch" : ""}`}
                >
                  <button
                    type="button"
                    class="rw-item-btn"
                    disabled={!jumpable || jumping}
                    onClick={() => onJump(point)}
                    aria-label={`${point.label || "(no message)"} — ${isTip ? "you are here" : "jump to rewind here"}`}
                  >
                    <span class="rw-node" aria-hidden="true">
                      {point.branchCount > 0 ? <GitBranch size={10} /> : null}
                    </span>
                    <span class="rw-body">
                      <span class="rw-label">{point.label || "(no message)"}</span>
                      <span class="rw-meta">
                        {formatRelativeTime(point.timestampMs)}
                        {point.branchCount > 0 && (
                          <span class="rw-chip">{point.branchCount} branch{point.branchCount === 1 ? "" : "es"}</span>
                        )}
                        {isTip && <span class="rw-here">you are here</span>}
                      </span>
                    </span>
                    {jumpable && !isTip && (
                      <span class="rw-jump">
                        <RewindIcon size={11} aria-hidden="true" />
                        {jumping ? "rewinding…" : "jump"}
                      </span>
                    )}
                  </button>
                </li>
              );
            })}
          </ul>
        )}

        <div class="rw-foot">
          Nothing is deleted — rewinding starts a new branch.
        </div>
      </div>
    </Sheet>
  );
}
