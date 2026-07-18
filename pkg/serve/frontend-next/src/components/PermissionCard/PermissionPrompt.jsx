import { useState, useEffect, useRef } from "preact/hooks";
import { PermissionCard } from "./PermissionCard.jsx";
import { resolvePermission, addPermissionRule } from "../../data/session-actions.js";
import { formatArgs } from "../../data/util/format.js";

// PermissionPrompt — stateful container around the presentational PermissionCard
// mock. Ports the semantics of the old SPA's permission-prompt-bar
// (pkg/serve/frontend/src/components/InputBar.jsx: permissionActive /
// handlePermissionResolve / handlePermissionRule) 1:1:
//   - Allow once  → resolvePermission(true)
//   - Always allow (only in permissionMode 'ask') → resolvePermission(true, {allow: allow_pattern})
//   - Deny        → resolvePermission(false)
//   - Add rule    (only in permissionMode 'auto') → addPermissionRule
//   - "+ feedback" toggles an inline input; its text always rides along in
//     resolvePermission's `feedback` option (not sent with add-rule, matching
//     the old bar).
// permBusy disables every action while a request is in flight; permError
// shows inline on failure. State resets whenever the pending permission's id
// changes (new prompt) or it disappears (resolved elsewhere/reconnect).
export function PermissionPrompt({ session }) {
  const perm = session.pendingPerm;
  const permissionMode = session.permissionMode || "yolo";

  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [feedbackOpen, setFeedbackOpen] = useState(false);
  const [feedback, setFeedback] = useState("");
  const [ruleOpen, setRuleOpen] = useState(false);
  const [rule, setRule] = useState("");
  // Synchronous in-flight guard: `busy` is reactive state, so two clicks before
  // the next render could both fire a resolve. This ref latches immediately and
  // is only released on error / new perm.id, guaranteeing a single resolution.
  const resolvingRef = useRef(false);

  useEffect(() => {
    setBusy(false);
    setError("");
    setFeedbackOpen(false);
    setFeedback("");
    setRuleOpen(false);
    setRule("");
    resolvingRef.current = false;
  }, [perm?.id]);

  if (!perm) return null;

  const resolve = async (approved, alwaysAllow = false) => {
    if (resolvingRef.current) return;
    resolvingRef.current = true;
    setBusy(true);
    setError("");
    try {
      await resolvePermission(session.id, perm.id, approved, {
        feedback: feedback.trim(),
        allow: alwaysAllow ? (perm.allow_pattern || "") : "",
      });
      setFeedbackOpen(false);
      setFeedback("");
      setRuleOpen(false);
      setRule("");
    } catch (e) {
      console.error("Permission resolve failed:", e);
      setError(e.message || "Permission resolve failed");
      resolvingRef.current = false;
      setBusy(false);
    }
  };

  const saveRule = async () => {
    const value = rule.trim();
    if (!value || resolvingRef.current) return;
    resolvingRef.current = true;
    setBusy(true);
    setError("");
    try {
      await addPermissionRule(session.id, perm.id, value);
      setRule("");
      setRuleOpen(false);
      resolvingRef.current = false;
      setBusy(false);
    } catch (e) {
      console.error("Add permission rule failed:", e);
      setError(e.message || "Could not add rule");
      resolvingRef.current = false;
      setBusy(false);
    }
  };

  const title = perm.tool_name ? `moa wants to run ${perm.tool_name}` : "moa wants to run";

  return (
    <PermissionCard
      title={title}
      command={formatArgs(perm.args)}
      alwaysLabel={permissionMode === "ask" ? perm.allow_pattern || undefined : undefined}
      disabled={busy}
      error={error}
      onAllow={() => resolve(true)}
      onAlways={() => resolve(true, true)}
      onDeny={() => resolve(false)}
      onFeedbackToggle={() => setFeedbackOpen((v) => !v)}
      feedbackActive={feedbackOpen}
      onRuleToggle={permissionMode === "auto" ? () => setRuleOpen((v) => !v) : undefined}
      ruleActive={ruleOpen}
    >
      {ruleOpen && permissionMode === "auto" && (
        <div class="perm-inline-editor">
          <input
            type="text"
            value={rule}
            onInput={(e) => setRule(e.currentTarget.value)}
            placeholder="Type rule and press Save rule"
            disabled={busy}
          />
          <button
            type="button"
            class="btn-rule-save perm-inline-save"
            disabled={busy || !rule.trim()}
            onClick={saveRule}
          >
            Save rule
          </button>
        </div>
      )}
      {feedbackOpen && (
        <div class="perm-inline-editor">
          <input
            type="text"
            value={feedback}
            onInput={(e) => setFeedback(e.currentTarget.value)}
            placeholder="Optional feedback"
            disabled={busy}
          />
        </div>
      )}
    </PermissionCard>
  );
}
