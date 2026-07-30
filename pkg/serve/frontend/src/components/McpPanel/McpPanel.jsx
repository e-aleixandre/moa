import { useState, useEffect, useCallback, useRef } from "preact/hooks";
import { RefreshCw, AlertTriangle, Check, ChevronRight, Lock } from "lucide-preact";
import { api, MCP_RESTART_TIMEOUT_MS } from "../../data/api.js";
import { addToast } from "../../data/notifications.js";
import "./McpPanel.css";

// McpPanel — the per-session MCP panel, "dossier" layout: the list at rest is
// one calm line per server (dot · name · tools · state pill · chevron), and
// expanding a server reveals its own detail, where all three scopes are
// configured together. There is deliberately NO panel-wide scope selector: the
// scope you are editing is always visible next to the switch you are touching,
// so no hidden mode changes the meaning of the whole list.
//
// Scopes are cumulative: a server runs only when every scope has it on
// (backend: disabled = global OR project OR session). Turning it on in one
// scope does not start it while another keeps it off — the panel says so in
// plain words rather than jargon, and never uses the word "veto" in the UI.
//
// The list is fetched from GET /api/sessions/{id}/mcp when the panel opens and
// re-fetched on every live mcp_change (mcpTick) and after a mutation, so rows
// never optimistically claim a process closed before the backend confirms.

// Scope rows, broadest last: the order tells the accumulation story.
const SCOPES = [
  { id: "session", label: "This session", why: "Only this conversation, until it ends" },
  { id: "project", label: "This project", why: "Writes .moa/config.json" },
  { id: "global", label: "Global", why: "Affects every project and future session" },
];

const SCOPE_NAME = { session: "Session", project: "Project", global: "Global" };

// STATE_META maps the technical runtime state to a label + severity class.
// "off" is a deliberate choice shown as neutral, never as an alarm;
// failed/exited alert; starting/disabling read as progress.
const STATE_META = {
  ready: { label: "ready", pill: "mcp-pill-ready", dot: "mcp-dot-ok" },
  starting: { label: "starting…", pill: "mcp-pill-prog", dot: "mcp-dot-busy" },
  disabling: { label: "turning off…", pill: "mcp-pill-prog", dot: "mcp-dot-busy" },
  disabled: { label: "off", pill: "mcp-pill-off", dot: "mcp-dot-off" },
  failed: { label: "failed", pill: "mcp-pill-bad", dot: "mcp-dot-bad" },
  exited: { label: "exited", pill: "mcp-pill-bad", dot: "mcp-dot-bad" },
};

// verdictFor builds the one-sentence truth at the top of an expanded server:
// what is true now and, when it is off, exactly what it takes to start it.
function verdictFor(server) {
  const off = server.disabled_scopes || [];
  if (server.pending_action) {
    const dir = server.pending_action === "disable" ? "Turning off" : "Turning on";
    return { text: `${dir} — applies when the current run finishes.`, names: [] };
  }
  if (off.length === 0) {
    if (server.state === "failed" || server.state === "exited") {
      return { text: "On everywhere, but it isn’t running — see the error below.", names: [] };
    }
    if (server.state === "ready") return { text: "On everywhere and running.", names: [] };
    return { text: "On everywhere.", names: [] };
  }
  const names = off.map((s) => SCOPE_NAME[s] || s);
  if (names.length === 1) {
    return { text: `Off — @0 keeps it off. Turn it on there to start it.`, names };
  }
  return { text: `Off — @0 and @1 keep it off. It starts once both are on.`, names };
}

// Renders a verdict sentence with its scope names emphasised.
function Verdict({ verdict }) {
  const parts = verdict.text.split(/(@\d)/);
  return (
    <div class="mcp-verdict">
      {parts.map((p, i) => {
        const m = /^@(\d)$/.exec(p);
        if (!m) return p;
        return <b key={i}>{verdict.names[Number(m[1])]}</b>;
      })}
    </div>
  );
}

function ScopeRow({ server, scope, on, writable, busy, onToggle, why }) {
  const label = `${server.name} in ${scope.label.toLowerCase()}: ${on ? "on" : "off"}`;
  return (
    <div class="mcp-scope-row">
      <div class="mcp-scope-key">
        <span class="mcp-scope-k">{scope.label}</span>
        <span class={`mcp-scope-why${writable ? "" : " mcp-scope-why-warn"}`}>{why}</span>
      </div>
      <button
        type="button"
        class={`mcp-schip${on ? " mcp-schip-on" : ""}`}
        aria-pressed={on}
        aria-label={label}
        disabled={busy || !writable}
        onClick={onToggle}
      >
        {on && (
          <span class="mcp-schip-check" aria-hidden="true">
            ✓
          </span>
        )}
        {on ? "On" : "Off"}
      </button>
    </div>
  );
}

function ServerDossier({ sessionId, server, projectWritable, onMutated }) {
  const [busy, setBusy] = useState(false);
  // Inline confirm for a broad, persistent change: {scope, next} or null.
  // Never window.confirm — that is a native dialog and breaks the PWA.
  const [confirming, setConfirming] = useState(null);

  const offScopes = server.disabled_scopes || [];
  const pending = server.pending_action || "";
  const verdict = verdictFor(server);

  // Restart only makes sense for an enabled, settled server: a disabled one has
  // no process, and one mid-transition would fight the reconcile.
  const canRestart =
    server.enabled &&
    server.state !== "starting" &&
    server.state !== "disabling" &&
    !pending;

  const apply = async (scopeId, nextOn) => {
    setBusy(true);
    try {
      await api("PATCH", `/api/sessions/${sessionId}/mcp/${encodeURIComponent(server.name)}`, {
        scope: scopeId,
        disabled: !nextOn,
      });
      onMutated();
    } catch (e) {
      addToast({
        title: `Could not update ${server.name}`,
        detail: String(e.message || e),
        type: "error",
      });
    } finally {
      setBusy(false);
      setConfirming(null);
    }
  };

  const requestToggle = (scopeId, nextOn) => {
    // Session scope is in-memory and reversible: act immediately. Project and
    // global persist and fan out, so they arm an inline confirm first.
    if (scopeId === "session") {
      apply(scopeId, nextOn);
      return;
    }
    setConfirming({ scope: scopeId, next: nextOn });
  };

  const restart = async () => {
    if (busy) return;
    setBusy(true);
    try {
      await api(
        "POST",
        `/api/sessions/${sessionId}/mcp/${encodeURIComponent(server.name)}/restart`,
        null,
        { timeoutMs: MCP_RESTART_TIMEOUT_MS }
      );
      onMutated();
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

  // Per-scope helper text: explain the consequence where it is not obvious,
  // above all when another scope is what actually keeps the server off.
  const whyFor = (scope, on) => {
    if (scope.id === "project" && !projectWritable) {
      return "Locked — project config isn’t trusted";
    }
    const others = offScopes.filter((s) => s !== scope.id).map((s) => SCOPE_NAME[s]);
    if (on && others.length > 0) {
      return `On here — but ${others.join(" and ")} still keeps it off`;
    }
    if (!on && others.length > 0) {
      return `Turning this on won’t start it — ${others.join(" and ")} is still off`;
    }
    return scope.why;
  };

  return (
    <div class="mcp-dossier">
      <Verdict verdict={verdict} />

      <div class="mcp-scopes">
        {SCOPES.map((scope) => {
          const on = !offScopes.includes(scope.id);
          const writable = scope.id === "project" ? projectWritable : true;
          return (
            <ScopeRow
              key={scope.id}
              server={server}
              scope={scope}
              on={on}
              writable={writable}
              busy={busy}
              why={whyFor(scope, on)}
              onToggle={() => requestToggle(scope.id, !on)}
            />
          );
        })}

        {confirming && (
          <div class="mcp-confirm">
            <p>
              <b>{confirming.scope === "global" ? "Global change" : "Project change"}</b> —{" "}
              {confirming.scope === "global"
                ? "affects every project and future session."
                : "writes .moa/config.json and affects open sessions here."}
            </p>
            <button
              type="button"
              class="mcp-cbtn mcp-cbtn-ghost"
              onClick={() => setConfirming(null)}
              disabled={busy}
            >
              Cancel
            </button>
            <button
              type="button"
              class="mcp-cbtn mcp-cbtn-go"
              onClick={() => apply(confirming.scope, confirming.next)}
              disabled={busy}
            >
              {confirming.next ? "Turn on" : "Turn off"}
            </button>
          </div>
        )}
      </div>

      {server.error && <div class="mcp-row-error">{server.error}</div>}

      <div class="mcp-dfoot">
        {canRestart && (
          <button
            type="button"
            class="mcp-restart"
            onClick={restart}
            disabled={busy}
            aria-label={`Restart ${server.name}`}
          >
            <RefreshCw size={13} aria-hidden="true" class={busy ? "mcp-spin" : ""} /> Restart
          </button>
        )}
        <span class="mcp-dfoot-meta">
          {server.state === "disabled"
            ? "no process while off"
            : server.tool_count === 1
              ? "1 tool"
              : `${server.tool_count || 0} tools`}
        </span>
      </div>
    </div>
  );
}

function ServerItem({ sessionId, server, projectWritable, onMutated, open, onOpenToggle }) {
  const meta = STATE_META[server.state] || {
    label: server.state,
    pill: "mcp-pill-bad",
    dot: "mcp-dot-bad",
  };
  const isOff = server.state === "disabled";
  const pending = server.pending_action || "";
  // A pending change is progress, whatever the settled state underneath.
  const pill = pending ? "mcp-pill-prog" : meta.pill;
  const dot = pending ? "mcp-dot-busy" : meta.dot;
  const label = pending
    ? `${meta.label} → ${pending === "disable" ? "off" : "on"}`
    : meta.label;

  return (
    <div
      class={`mcp-item${isOff ? " mcp-item-off" : ""}${open ? " mcp-item-open" : ""}`}
    >
      <button
        type="button"
        class="mcp-head-btn"
        aria-expanded={open}
        onClick={onOpenToggle}
      >
        <span class={`mcp-dot ${dot}`} aria-hidden="true" />
        <span class="mcp-name">{server.name}</span>
        <span class="mcp-tools">
          {isOff ? "–" : server.tool_count === 1 ? "1 tool" : `${server.tool_count || 0} tools`}
        </span>
        <span class={`mcp-pill ${pill}`}>{label}</span>
        <ChevronRight size={13} aria-hidden="true" class="mcp-chev" />
      </button>
      {open && (
        <ServerDossier
          sessionId={sessionId}
          server={server}
          projectWritable={projectWritable}
          onMutated={onMutated}
        />
      )}
    </div>
  );
}

export function McpPanel({ sessionId, mcpTick, variant }) {
  const [data, setData] = useState(null); // null = loading; {servers, available_scopes, ...}
  const [failed, setFailed] = useState(false);
  const [openName, setOpenName] = useState(null); // which server's dossier is open

  // Guards against a stale GET landing after the session changed or the panel
  // unmounted: each load captures the session it was issued for and is dropped
  // if that is no longer the current one. reqSeq also drops an older in-flight
  // load superseded by a newer one (e.g. a fast mcpTick after switching).
  const reqSeqRef = useRef(0);

  // Tracks the panel's current session for async callbacks that captured an
  // older `load` (e.g. a mutation that resolves after a session switch): they
  // must not commit stale rows into a different session's panel. Assigned
  // synchronously during render (not in a passive effect) so there is no
  // render-to-effect window where an A-load could pass the guard after the
  // panel has switched to B.
  const liveSessionRef = useRef(sessionId);
  liveSessionRef.current = sessionId;

  const load = useCallback(() => {
    if (!sessionId) return;
    const seq = ++reqSeqRef.current;
    const forSession = sessionId;
    api("GET", `/api/sessions/${sessionId}/mcp`)
      .then((r) => {
        // Drop if superseded by a newer load or if the panel moved to another
        // session since this load was issued (liveSessionRef is always current).
        if (seq !== reqSeqRef.current || forSession !== liveSessionRef.current) return;
        setData(r && Array.isArray(r.servers) ? r : { servers: [], available_scopes: {} });
        setFailed(false);
      })
      .catch(() => {
        if (seq !== reqSeqRef.current || forSession !== liveSessionRef.current) return;
        setFailed(true);
      });
  }, [sessionId]);

  // Reset the expanded row when the panel is (re)mounted for another session.
  useEffect(() => {
    setOpenName(null);
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

  const onMutated = useCallback(() => {
    // Always re-fetch: a mutation can change scopes/summary and other rows
    // (fan-out), and we never optimistically claim a process closed.
    load();
  }, [load]);

  if (failed) return <div class="mcp-empty">Couldn’t load MCP servers.</div>;
  if (data === null) return <div class="mcp-empty">Loading…</div>;
  const servers = data.servers || [];
  if (servers.length === 0) return <div class="mcp-empty">No MCP servers for this session.</div>;

  const scopes = data.available_scopes || {};
  const projectWritable = scopes.project?.writable !== false;
  const healthy = servers.every((s) => s.state === "ready" || s.state === "disabled");
  const anyOff = servers.some((s) => s.state === "disabled");

  return (
    <div class={`mcp-body${variant === "sheet" ? " mcp-sheet" : ""}`}>
      <div class="mcp-summary">
        {healthy ? (
          <>
            <Check size={13} aria-hidden="true" />{" "}
            {anyOff ? "All active servers ready" : "All servers ready"}
          </>
        ) : (
          <>
            <AlertTriangle size={13} aria-hidden="true" /> Some servers need attention
          </>
        )}
      </div>

      {!projectWritable && (
        <div class="mcp-summary">
          <Lock size={13} aria-hidden="true" /> Project scope is locked — this project’s config
          isn’t trusted.
        </div>
      )}

      <div class="mcp-list">
        {servers.map((s) => (
          <ServerItem
            key={s.name}
            sessionId={sessionId}
            server={s}
            projectWritable={projectWritable}
            onMutated={onMutated}
            open={openName === s.name}
            onOpenToggle={() => setOpenName(openName === s.name ? null : s.name)}
          />
        ))}
      </div>
    </div>
  );
}
