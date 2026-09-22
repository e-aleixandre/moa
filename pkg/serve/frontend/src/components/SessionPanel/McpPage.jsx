import { useState, useEffect, useCallback, useRef } from "preact/hooks";
import { api, MCP_RESTART_TIMEOUT_MS } from "../../data/api.js";
import { addToast } from "../../data/notifications.js";
import { openBlankWindow, startConnect, finishConnect, oauthErrorText } from "./mcp-oauth-flow.js";

// McpPage — the dossier's MCP page. Markup is the catalogue's
// (catalog/zones-lab.jsx `McpPage`, classes `.zl-mcp*` / `.zl-scope*` /
// `.zl-switch`), grafted onto the production fetch, the three-scope toggle
// with confirm for project/global, restart, and sign-in for remote servers.

const SCOPES = [
  { id: "session", label: "This session", why: "Only this conversation, until it ends" },
  { id: "project", label: "This project", why: "Whenever you work in this project" },
  { id: "global", label: "Global", why: "Every project and future session" },
];

const SCOPE_NAME = { session: "Session", project: "Project", global: "Global" };

const MCP_STATE = {
  ready: ["running", "is-ok"],
  failed: ["failed", "is-bad"],
  exited: ["exited", "is-bad"],
  disabled: ["off", "is-off"],
  starting: ["starting…", "is-busy"],
  disabling: ["turning off…", "is-busy"],
  // Waiting for the user to sign in: it needs you, it is not down.
  auth_required: ["needs sign-in", "is-need"],
};

const authLabel = (server) => (server.auth_action === "reconnect" ? "Reconnect" : "Connect");

function Switch({ on, onChange, label, disabled }) {
  return (
    <button
      type="button"
      class={`zl-switch${on ? " is-on" : ""}`}
      role="switch"
      aria-checked={on}
      aria-label={label}
      disabled={disabled}
      onClick={onChange ? () => onChange(!on) : undefined}
    >
      <span class="zl-switch-track" aria-hidden="true"><span class="zl-switch-knob" /></span>
    </button>
  );
}

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
    if (server.state === "auth_required") {
      return { text: "On everywhere, waiting for you to sign in.", names: [] };
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

function Verdict({ verdict }) {
  const parts = verdict.text.split(/(@\d)/);
  return (
    <p class="zl-mcp-verdict">
      {parts.map((p, i) => {
        const m = /^@(\d)$/.exec(p);
        if (!m) return p;
        return <b key={i}>{verdict.names[Number(m[1])]}</b>;
      })}
    </p>
  );
}

function whyFor(server, scope, on) {
  const offScopes = server.disabled_scopes || [];
  const others = offScopes.filter((s) => s !== scope.id).map((s) => SCOPE_NAME[s]);
  if (on && others.length > 0) {
    return `On here — but ${others.join(" and ")} still keeps it off`;
  }
  if (!on && others.length > 0) {
    return `Turning this on won’t start it — ${others.join(" and ")} is still off`;
  }
  return scope.why;
}

function ServerBody({ sessionId, server, onMutated, inline, onLocalToggle }) {
  const [busy, setBusy] = useState(false);
  const [confirming, setConfirming] = useState(null);
  // Sign-in in progress: { url, opened, pasted, error } or null.
  const [oauth, setOauth] = useState(null);
  const offScopes = server.disabled_scopes || [];
  const pending = server.pending_action || "";
  const verdict = verdictFor(server);
  const canRestart =
    server.enabled !== false &&
    server.state !== "starting" &&
    server.state !== "disabling" &&
    server.state !== "disabled" &&
    !pending;

  const apply = async (scopeId, nextOn) => {
    if (inline) {
      onLocalToggle?.(server.name, scopeId, nextOn);
      setConfirming(null);
      return;
    }
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
    if (inline || scopeId === "session") {
      apply(scopeId, nextOn);
      return;
    }
    setConfirming({ scope: scopeId, next: nextOn });
  };

  const restart = async () => {
    if (busy || inline) return;
    setBusy(true);
    try {
      await api(
        "POST",
        `/api/sessions/${sessionId}/mcp/${encodeURIComponent(server.name)}/restart`,
        null,
        { timeoutMs: MCP_RESTART_TIMEOUT_MS },
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

  const needsAuth = server.state === "auth_required" && !pending;

  const connect = async () => {
    if (busy || inline) return;
    // Opened here, inside the click, so popup blockers allow it; startConnect
    // routes it to the sign-in page once the URL arrives.
    const handle = openBlankWindow();
    setBusy(true);
    setOauth(null);
    try {
      const { url, opened } = await startConnect(api, sessionId, server.name, handle);
      setOauth({ url, opened, pasted: "", error: "" });
    } catch (e) {
      setOauth({ url: "", opened: false, pasted: "", error: oauthErrorText(e) });
    } finally {
      setBusy(false);
    }
  };

  const finish = async (ev) => {
    ev?.preventDefault?.();
    const pasted = oauth?.pasted?.trim() || "";
    if (busy || inline || !pasted) return;
    setBusy(true);
    setOauth((o) => o && { ...o, error: "" });
    try {
      const st = await finishConnect(api, sessionId, server.name, pasted);
      if (st && st.state === "auth_required") {
        setOauth({
          url: "",
          opened: false,
          pasted: "",
          error: `Sign-in didn’t work. Press ${authLabel(st)} again.`,
        });
      } else {
        setOauth(null);
      }
      onMutated();
    } catch (e) {
      setOauth((o) => o && { ...o, error: oauthErrorText(e) });
    } finally {
      setBusy(false);
    }
  };

  const tools = server.tools ?? server.tool_count ?? 0;
  const footMeta = server.foot
    || (server.state === "disabled" ? "no process while off" : `${tools} tools`);

  return (
    <div class="zl-mcp-body">
      <Verdict verdict={verdict} />
      {SCOPES.map((sc) => {
        const on = !offScopes.includes(sc.id);
        return (
          <div class="zl-scope" key={sc.id}>
            <span class="zl-scope-txt">
              <span class="zl-scope-k">{sc.label}</span>
              <span class="zl-scope-why">{(server.whys && server.whys[sc.id]) || whyFor(server, sc, on)}</span>
            </span>
            <Switch
              on={on}
              disabled={busy}
              onChange={(v) => requestToggle(sc.id, v)}
              label={`${server.name} in ${sc.label}`}
            />
          </div>
        );
      })}
      {confirming && (
        <div class="zl-mcp-confirm">
          <p>
            <b>{confirming.scope === "global" ? "Global change" : "Project change"}</b>
            {" — "}
            {confirming.scope === "global"
              ? "affects every project and future session."
              : "applies whenever you work in this project, and to open sessions here."}
          </p>
          <div class="zl-mcp-confirm-acts">
            <button type="button" class="zl-btn" onClick={() => setConfirming(null)} disabled={busy}>Cancel</button>
            <button type="button" class="zl-btn" onClick={() => apply(confirming.scope, confirming.next)} disabled={busy}>
              {confirming.next ? "Turn on" : "Turn off"}
            </button>
          </div>
        </div>
      )}
      {server.error && server.state !== "auth_required" && (
        <div class="zl-mcp-err zl-data">{server.error}</div>
      )}
      <div class="zl-mcp-foot">
        {needsAuth ? (
          <button type="button" class="zl-btn" onClick={connect} disabled={busy} aria-label={`${authLabel(server)} ${server.name}`}>
            {authLabel(server)}
          </button>
        ) : canRestart ? (
          <button type="button" class="zl-btn" onClick={restart} disabled={busy} aria-label={`Restart ${server.name}`}>
            Restart
          </button>
        ) : <span />}
        <span class="zl-kv-hint zl-data">{footMeta}</span>
      </div>
      {needsAuth && oauth && (
        <div class="zl-mcp-oauth">
          {oauth.url && (
            <form class="zl-mcp-oauth-form" onSubmit={finish} noValidate>
              {!oauth.opened && (
                <a class="zl-mcp-oauth-link" href={oauth.url} target="_blank" rel="noopener noreferrer">
                  Open sign-in page
                </a>
              )}
              <p class="zl-mcp-oauth-hint">Sign in, then paste the address of the page that doesn’t load.</p>
              <div class="zl-mcp-oauth-row">
                <input
                  class="zl-input"
                  type="url"
                  inputMode="url"
                  autoComplete="off"
                  autoCapitalize="off"
                  autoCorrect="off"
                  spellcheck={false}
                  placeholder="Paste the address"
                  aria-label="Address of the page that doesn’t load"
                  value={oauth.pasted}
                  disabled={busy}
                  onInput={(e) => {
                    const value = e.currentTarget.value;
                    setOauth((o) => o && { ...o, pasted: value });
                  }}
                />
                <button type="submit" class="zl-btn" disabled={busy || !oauth.pasted.trim()}>
                  Finish
                </button>
              </div>
            </form>
          )}
          {oauth.error && (
            <p class="zl-mcp-oauth-err" role="alert">{oauth.error}</p>
          )}
        </div>
      )}
    </div>
  );
}

export function McpPage({ sessionId, mcpTick, servers: fixtureServers, inline = false }) {
  const [data, setData] = useState(fixtureServers ? { servers: fixtureServers } : null);
  const [failed, setFailed] = useState(false);
  const [open, setOpen] = useState(() => {
    const list = fixtureServers || [];
    const bad = list.find((s) => s.state === "failed" || s.state === "exited" || s.state === "auth_required");
    return bad ? bad.name : (list[0] && list.length === 1 ? list[0].name : null);
  });
  const reqSeqRef = useRef(0);
  const liveSessionRef = useRef(sessionId);
  liveSessionRef.current = sessionId;

  const load = useCallback(() => {
    if (fixtureServers || !sessionId) return;
    const seq = ++reqSeqRef.current;
    const forSession = sessionId;
    api("GET", `/api/sessions/${sessionId}/mcp`)
      .then((r) => {
        if (seq !== reqSeqRef.current || forSession !== liveSessionRef.current) return;
        setData(r && Array.isArray(r.servers) ? r : { servers: [] });
        setFailed(false);
      })
      .catch(() => {
        if (seq !== reqSeqRef.current || forSession !== liveSessionRef.current) return;
        setFailed(true);
      });
  }, [sessionId, fixtureServers]);

  useEffect(() => {
    if (fixtureServers) {
      setData({ servers: fixtureServers });
      return undefined;
    }
    setOpen(null);
    setData(null);
    load();
    return () => { reqSeqRef.current++; };
  }, [load, mcpTick, sessionId, fixtureServers]);

  const onLocalToggle = (name, scopeId, nextOn) => {
    setData((cur) => {
      const servers = (cur?.servers || []).map((s) => {
        if (s.name !== name) return s;
        const off = new Set(s.disabled_scopes || []);
        if (nextOn) off.delete(scopeId);
        else off.add(scopeId);
        return { ...s, disabled_scopes: [...off] };
      });
      return { servers };
    });
  };

  if (failed) return <p class="zl-page-sum">Couldn’t load MCP servers.</p>;
  if (data === null) return <p class="zl-page-sum">Loading…</p>;
  const servers = data.servers || [];
  if (servers.length === 0) return <p class="zl-page-sum">No MCP servers for this session.</p>;

  const up = servers.filter((s) => s.state === "ready").length;

  return (
    <div class="zl-page">
      <p class="zl-page-sum">
        <span class="zl-data">{up}</span> of <span class="zl-data">{servers.length}</span> running. A server runs only when every scope has it on.
      </p>
      <div class="zl-kv">
        {servers.map((s) => {
          const [label, tone] = MCP_STATE[s.state] || [s.state, "is-bad"];
          const isOpen = open === s.name;
          const tools = s.tools ?? s.tool_count ?? 0;
          return (
            <div class={`zl-mcp${isOpen ? " is-open" : ""}`} key={s.name}>
              <button
                type="button"
                class="zl-kv-row is-btn zl-mcp-head"
                onClick={() => setOpen(isOpen ? null : s.name)}
                aria-expanded={isOpen}
              >
                <span class={`zl-mcp-dot ${tone}`} aria-hidden="true" />
                <span class="zl-kv-k is-strong">{s.name}</span>
                <span class="zl-kv-hint zl-data">{tools} tools</span>
                <span class={`zl-mcp-state ${tone}`}>{label}</span>
                <svg class={`zl-go${isOpen ? " is-open" : ""}`} viewBox="0 0 12 12" aria-hidden="true">
                  <path d="M4 2.5L7.5 6 4 9.5" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" />
                </svg>
              </button>
              {isOpen && (
                <ServerBody
                  sessionId={sessionId}
                  server={s}
                  onMutated={load}
                  inline={inline}
                  onLocalToggle={onLocalToggle}
                />
              )}
            </div>
          );
        })}
      </div>
    </div>
  );
}
