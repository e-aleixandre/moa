import { useState, useEffect, useCallback } from "preact/hooks";
import { RefreshCw, Plug, AlertTriangle, Check } from "lucide-preact";
import { api } from "../../data/api.js";
import { addToast } from "../../data/notifications.js";
import "./McpPanel.css";

// McpPanel — the per-session MCP panel: one row per configured server with
// its state (ready / failed / exited / restarting), its tool count, any error,
// and a Restart button. The list is fetched from GET /api/sessions/{id}/mcp when
// the sheet opens (the status-line indicator's summary is glanceable-only), and
// re-fetched after a restart so the row reflects the fresh state.
//
// Per-session, not global, because the runtime is per-session: a server that
// crashed here has its own process, and restarting it touches only this
// session. No scope/shared columns — none exist.

const STATE_META = {
  ready: { label: "ready", cls: "mcp-st-ready" },
  restarting: { label: "restarting…", cls: "mcp-st-restarting" },
  failed: { label: "failed", cls: "mcp-st-bad" },
  exited: { label: "exited", cls: "mcp-st-bad" },
};

function ServerRow({ sessionId, server, onUpdated }) {
  const [busy, setBusy] = useState(false);
  const meta = STATE_META[server.state] || { label: server.state, cls: "mcp-st-bad" };
  const bad = server.state === "failed" || server.state === "exited";

  const restart = async () => {
    if (busy) return;
    setBusy(true);
    try {
      const fresh = await api("POST", `/api/sessions/${sessionId}/mcp/${encodeURIComponent(server.name)}/restart`);
      onUpdated(fresh);
    } catch (e) {
      addToast({
        title: `Could not restart ${server.name}`,
        detail: String(e.message || e),
        type: "error",
      });
    } finally {
      setBusy(false);
    }
  };

  return (
    <div class={`mcp-row${bad ? " mcp-row-bad" : ""}`}>
      <div class="mcp-row-head">
        <span class="mcp-row-name">
          {bad ? <AlertTriangle size={13} aria-hidden="true" /> : <Plug size={13} aria-hidden="true" />}
          {server.name}
        </span>
        <span class={`mcp-row-state ${meta.cls}`}>{busy ? "restarting…" : meta.label}</span>
      </div>
      <div class="mcp-row-meta">
        <span class="mcp-row-tools">
          {server.tool_count === 1 ? "1 tool" : `${server.tool_count || 0} tools`}
        </span>
        <button
          type="button"
          class="mcp-row-restart"
          onClick={restart}
          disabled={busy}
          aria-label={`Restart ${server.name}`}
        >
          <RefreshCw size={13} aria-hidden="true" class={busy ? "mcp-spin" : ""} /> Restart
        </button>
      </div>
      {server.error && <div class="mcp-row-error">{server.error}</div>}
    </div>
  );
}

export function McpPanel({ sessionId, mcpTick }) {
  const [servers, setServers] = useState(null); // null = loading
  const [failed, setFailed] = useState(false);

  const load = useCallback(() => {
    if (!sessionId) return;
    api("GET", `/api/sessions/${sessionId}/mcp`)
      .then((r) => {
        setServers(Array.isArray(r?.servers) ? r.servers : []);
        setFailed(false);
      })
      .catch(() => setFailed(true));
  }, [sessionId]);

  // Reload on open and whenever a live mcp_change event bumps the tick, so a
  // server that crashed, recovered, or was toggled elsewhere reflects here
  // without the user reopening the sheet.
  useEffect(() => {
    load();
  }, [load, mcpTick]);

  const onUpdated = (fresh) => {
    // Splice the restarted server's fresh status in place; a full reload would
    // also work but would blink the whole list.
    setServers((prev) =>
      (prev || []).map((s) => (s.name === fresh.name ? fresh : s)),
    );
  };

  if (failed) {
    return <div class="mcp-empty">Couldn’t load MCP servers.</div>;
  }
  if (servers === null) {
    return <div class="mcp-empty">Loading…</div>;
  }
  if (servers.length === 0) {
    return <div class="mcp-empty">No MCP servers for this session.</div>;
  }

  const healthy = servers.every((s) => s.state === "ready");
  return (
    <div class="mcp-body">
      <div class="mcp-summary">
        {healthy ? (
          <>
            <Check size={13} aria-hidden="true" /> All servers ready
          </>
        ) : (
          <>
            <AlertTriangle size={13} aria-hidden="true" /> Some servers need attention
          </>
        )}
      </div>
      <div class="mcp-list">
        {servers.map((s) => (
          <ServerRow key={s.name} sessionId={sessionId} server={s} onUpdated={onUpdated} />
        ))}
      </div>
    </div>
  );
}
