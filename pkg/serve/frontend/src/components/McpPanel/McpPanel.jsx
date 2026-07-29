import { useState, useEffect, useCallback, useRef } from "preact/hooks";
import { RefreshCw, Plug, AlertTriangle, Check } from "lucide-preact";
import { api, MCP_RESTART_TIMEOUT_MS } from "../../data/api.js";
import { addToast } from "../../data/notifications.js";
import "./McpPanel.css";

// McpPanel — the per-session MCP panel: one row per configured server with its
// state, tool count, any error, veto badges, a Restart button, and a
// disable/enable toggle scoped to the selected level (This session / This
// project / Global). The list is fetched from GET /api/sessions/{id}/mcp when
// the sheet opens and re-fetched on every live mcp_change (mcpTick) and after a
// mutation, so rows never optimistically claim a process closed before the
// backend confirms.
//
// Scopes are cumulative vetoes: disabled = global OR project OR session. A row
// can be desired-disabled by several scopes at once (shown as badges); toggling
// the selected scope changes only that membership, which is why removing one
// veto may leave the server still disabled by another.

const SCOPES = [
  { id: "session", label: "This session" },
  { id: "project", label: "This project" },
  { id: "global", label: "Global" },
];

// STATE_META maps the technical runtime state to a label + severity class.
// Disabled is neutral (a deliberate choice), not an alarm; failed/exited alert;
// starting/disabling show progress.
const STATE_META = {
  ready: { label: "ready", cls: "mcp-st-ready" },
  starting: { label: "starting…", cls: "mcp-st-progress" },
  disabling: { label: "disabling…", cls: "mcp-st-progress" },
  disabled: { label: "disabled", cls: "mcp-st-disabled" },
  failed: { label: "failed", cls: "mcp-st-bad" },
  exited: { label: "exited", cls: "mcp-st-bad" },
};

const SCOPE_BADGE = { global: "Global", project: "Project", session: "Session" };

function ServerRow({ sessionId, server, scope, scopeWritable, onMutated, requestConfirm }) {
  const [busy, setBusy] = useState(false);
  const meta = STATE_META[server.state] || { label: server.state, cls: "mcp-st-bad" };
  const bad = server.state === "failed" || server.state === "exited";
  const scopes = server.disabled_scopes || [];
  const disabledInScope = scopes.includes(scope);
  const pending = server.pending_action || "";
  // Restart only makes sense for an enabled, settled server: a disabled one has
  // no process, and one mid-transition would fight the reconcile.
  const canRestart =
    server.enabled &&
    server.state !== "starting" &&
    server.state !== "disabling" &&
    !pending;

  const restart = async () => {
    if (busy) return;
    setBusy(true);
    try {
      const fresh = await api("POST", `/api/sessions/${sessionId}/mcp/${encodeURIComponent(server.name)}/restart`, null, { timeoutMs: MCP_RESTART_TIMEOUT_MS });
      onMutated(fresh);
    } catch (e) {
      addToast({ title: `Could not restart ${server.name}`, detail: String(e.message || e), type: "error" });
    } finally {
      setBusy(false);
    }
  };

  const toggle = async () => {
    if (busy || !scopeWritable) return;
    const nextDisabled = !disabledInScope;
    const ok = await requestConfirm(scope, nextDisabled);
    if (!ok) return;
    setBusy(true);
    try {
      const res = await api("PATCH", `/api/sessions/${sessionId}/mcp/${encodeURIComponent(server.name)}`, {
        scope,
        disabled: nextDisabled,
      });
      onMutated(res?.server);
    } catch (e) {
      addToast({ title: `Could not update ${server.name}`, detail: String(e.message || e), type: "error" });
    } finally {
      setBusy(false);
    }
  };

  return (
    <div class={`mcp-row${bad ? " mcp-row-bad" : ""}${server.state === "disabled" ? " mcp-row-off" : ""}`}>
      <div class="mcp-row-head">
        <span class="mcp-row-name">
          {bad ? <AlertTriangle size={13} aria-hidden="true" /> : <Plug size={13} aria-hidden="true" />}
          {server.name}
        </span>
        <span class={`mcp-row-state ${meta.cls}`}>{busy && !pending ? "working…" : meta.label}</span>
      </div>

      {scopes.length > 0 && (
        <div class="mcp-row-badges">
          {SCOPES.map((s) =>
            scopes.includes(s.id) ? (
              <span key={s.id} class="mcp-badge" title={`Disabled in ${s.label}`}>
                {SCOPE_BADGE[s.id]}
              </span>
            ) : null,
          )}
        </div>
      )}

      {pending && (
        <div class="mcp-row-pending">
          Pending {pending} — applies when work finishes
        </div>
      )}

      <div class="mcp-row-meta">
        <label class={`mcp-toggle${scopeWritable ? "" : " mcp-toggle-off"}`}>
          <input
            type="checkbox"
            checked={disabledInScope}
            disabled={busy || !scopeWritable}
            onChange={toggle}
          />
          <span>Disabled in {SCOPES.find((s) => s.id === scope)?.label.toLowerCase()}</span>
        </label>
        {canRestart && (
          <button
            type="button"
            class="mcp-row-restart"
            onClick={restart}
            disabled={busy}
            aria-label={`Restart ${server.name}`}
          >
            <RefreshCw size={13} aria-hidden="true" class={busy ? "mcp-spin" : ""} /> Restart
          </button>
        )}
      </div>

      <div class="mcp-row-foot">
        <span class="mcp-row-tools">
          {server.tool_count === 1 ? "1 tool" : `${server.tool_count || 0} tools`}
        </span>
      </div>

      {server.error && <div class="mcp-row-error">{server.error}</div>}
    </div>
  );
}

export function McpPanel({ sessionId, mcpTick }) {
  const [data, setData] = useState(null); // null = loading; {servers, available_scopes, ...}
  const [failed, setFailed] = useState(false);
  const [scope, setScope] = useState("session"); // safe default on open
  // Track which broad scopes were already confirmed this open, so we don't
  // re-prompt for every toggle in the same session of interaction.
  const confirmedRef = useRef({});

  // Guards against a stale GET landing after the session changed or the panel
  // unmounted: each load captures the session it was issued for and is dropped
  // if that is no longer the current one. reqSeq also drops an older in-flight
  // load superseded by a newer one (e.g. a fast mcpTick after switching).
  const reqSeqRef = useRef(0);

  const load = useCallback(() => {
    if (!sessionId) return;
    const seq = ++reqSeqRef.current;
    const forSession = sessionId;
    api("GET", `/api/sessions/${sessionId}/mcp`)
      .then((r) => {
        if (seq !== reqSeqRef.current || forSession !== sessionId) return; // superseded
        setData(r && Array.isArray(r.servers) ? r : { servers: [], available_scopes: {} });
        setFailed(false);
      })
      .catch(() => {
        if (seq !== reqSeqRef.current || forSession !== sessionId) return;
        setFailed(true);
      });
  }, [sessionId]);

  // Reload on open, on live mcp_change (mcpTick), and reset confirmations +
  // scope each time the panel is (re)mounted for a session.
  useEffect(() => {
    confirmedRef.current = {};
    setScope("session");
    setData(null); // don't show the previous session's servers while reloading
  }, [sessionId]);
  useEffect(() => {
    load();
    // Invalidate any in-flight load when the session changes or we unmount, so
    // its late response is ignored.
    return () => {
      reqSeqRef.current++;
    };
  }, [load, mcpTick]);

  const scopes = data?.available_scopes || {};
  const projectWritable = scopes.project?.writable !== false;
  const scopeWritable = scope === "project" ? projectWritable : true;

  // requestConfirm gates the first broad-scope action per open. Session scope
  // is local and needs no confirmation. Returns a promise resolving to whether
  // the action should proceed.
  const requestConfirm = useCallback((sc, nextDisabled) => {
    if (sc === "session") return Promise.resolve(true);
    if (confirmedRef.current[sc]) return Promise.resolve(true);
    const verb = nextDisabled ? "Disable" : "Enable";
    const msg =
      sc === "global"
        ? `${verb} for Global scope — affects every project and future session. Continue?`
        : `${verb} for This project — writes ${data?.cwd || "the project"}/.moa/config.json and affects open sessions in this project. Continue?`;
    // eslint-disable-next-line no-alert -- deliberate confirm for a broad, persistent action
    const ok = typeof window !== "undefined" && window.confirm(msg);
    if (ok) confirmedRef.current[sc] = true;
    return Promise.resolve(ok);
  }, [data]);

  const onMutated = useCallback(() => {
    // Always re-fetch: a mutation can change scopes/summary and other rows
    // (fan-out), and we never optimistically claim a process closed.
    load();
  }, [load]);

  if (failed) return <div class="mcp-empty">Couldn’t load MCP servers.</div>;
  if (data === null) return <div class="mcp-empty">Loading…</div>;
  const servers = data.servers || [];
  if (servers.length === 0) return <div class="mcp-empty">No MCP servers for this session.</div>;

  const healthy = servers.every((s) => s.state === "ready" || s.state === "disabled");
  const anyDisabled = servers.some((s) => s.state === "disabled");

  return (
    <div class="mcp-body">
      <div class="mcp-scopebar">
        <span class="mcp-scopebar-label">Scope</span>
        <select
          class="mcp-scope-select"
          value={scope}
          onChange={(e) => setScope(e.currentTarget.value)}
          aria-label="Disable scope"
        >
          {SCOPES.map((s) => (
            <option key={s.id} value={s.id}>{s.label}</option>
          ))}
        </select>
      </div>

      {scope === "project" && !projectWritable && (
        <div class="mcp-scope-note">Project config is not trusted; trust it to change project scope.</div>
      )}

      <div class="mcp-summary">
        {healthy ? (
          <>
            <Check size={13} aria-hidden="true" /> {anyDisabled ? "All active servers ready" : "All servers ready"}
          </>
        ) : (
          <>
            <AlertTriangle size={13} aria-hidden="true" /> Some servers need attention
          </>
        )}
      </div>

      <div class="mcp-list">
        {servers.map((s) => (
          <ServerRow
            key={s.name}
            sessionId={sessionId}
            server={s}
            scope={scope}
            scopeWritable={scopeWritable}
            onMutated={onMutated}
            requestConfirm={requestConfirm}
          />
        ))}
      </div>
    </div>
  );
}
