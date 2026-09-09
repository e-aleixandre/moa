import { useEffect, useRef, useState } from "preact/hooks";
import "./zones-lab.css";

/* The three-zone skeleton, both densities side by side.
   This is a PROTOTYPE, not production: it draws the shell only (where things
   live and how they open), with the real transcript stubbed as grey bars. The
   point is to judge the architecture -- left = the other sessions, top-centre =
   this session, bottom = state -- before any of it is built for real. */

const SESSIONS = [
  { title: "Buscar un bug bounty", when: "now", path: "~/dev", state: "running" },
  { title: "Check access to two repos", when: "28m", path: "~/dev", state: "needs" },
  { title: "Limpiar Docker y worktrees", when: "35m", path: "~/dev", state: "idle" },
  { title: "Búscame un", when: "36m", path: "~/dev", state: "idle" },
  { title: "Browse Gugo GitLab", when: "39d", path: "~/dev", state: "idle" },
  { title: "MenuApp", when: "41d", path: "~/dev", state: "idle" },
];

function Dot({ state }) {
  return <span class={`zl-dot is-${state}`} aria-hidden="true" />;
}

function SessionList({ onPick }) {
  return (
    <div class="zl-list">
      <div class="zl-group">Active</div>
      {SESSIONS.slice(0, 2).map((s) => (
        <button type="button" class="zl-row" onClick={onPick} key={s.title}>
          <Dot state={s.state} />
          <span class="zl-row-main">
            <span class="zl-row-title">{s.title}</span>
            <span class="zl-row-path">{s.path}</span>
          </span>
          <span class="zl-row-when">{s.when}</span>
        </button>
      ))}
      <div class="zl-group">Saved</div>
      {SESSIONS.slice(2).map((s) => (
        <button type="button" class="zl-row" onClick={onPick} key={s.title}>
          <Dot state={s.state} />
          <span class="zl-row-main">
            <span class="zl-row-title">{s.title}</span>
            <span class="zl-row-path">{s.path}</span>
          </span>
          <span class="zl-row-when">{s.when}</span>
        </button>
      ))}
    </div>
  );
}

/* The session panel: what the chip opens. Deliberately excludes model,
   permissions and fast -- those already live in the status line, and putting
   them here too would break "one datum, one place". */
function SessionPanel({ onClose }) {
  return (
    <div class="zl-panel" role="dialog" aria-label="Session">
      <div class="zl-panel-head">
        <span>Session</span>
        <button type="button" class="zl-x" onClick={onClose} aria-label="Close">×</button>
      </div>
      <label class="zl-field">
        <span class="zl-label">Name</span>
        <input class="zl-input" defaultValue="Buscar un bug bounty" />
      </label>
      <label class="zl-field">
        <span class="zl-label">Folder</span>
        <input class="zl-input" defaultValue="~/dev/moa/main" readOnly />
      </label>
      <div class="zl-panel-note">
        Model, permissions and fast stay in the status line below — one datum,
        one place.
      </div>
      <div class="zl-panel-acts">
        <button type="button" class="zl-btn">Save for later</button>
        <button type="button" class="zl-btn is-danger">Close session</button>
      </div>
    </div>
  );
}

function Transcript() {
  return (
    <div class="zl-transcript">
      <div class="zl-bar" style="width:72%" />
      <div class="zl-bar" style="width:90%" />
      <div class="zl-bar" style="width:54%" />
      <div class="zl-user">
        <div class="zl-bar is-on-user" style="width:60%" />
      </div>
      <div class="zl-bar" style="width:84%" />
      <div class="zl-bar" style="width:66%" />
      <div class="zl-bar" style="width:78%" />
      <div class="zl-bar" style="width:40%" />
    </div>
  );
}

function StatusLine() {
  return (
    <div class="zl-status">
      <span class="zl-ctx">ctx 10%</span>
      <span class="zl-model">Daybreak Blue</span>
      <span class="zl-yolo">yolo</span>
      <span class="zl-spacer" />
      <span class="zl-tok">↑0 ·↓0 tok</span>
    </div>
  );
}

/* ── Phone ─────────────────────────────────────────────────────────────── */
function Phone({ label }) {
  const [sidebar, setSidebar] = useState(false);
  const [panel, setPanel] = useState(false);
  const [drag, setDrag] = useState(0);
  const startRef = useRef(null);

  // The edge gesture, prototyped: 24px zone, abandons if the movement turns
  // vertical. Enough to feel whether "swipe opens the sidebar" is right.
  const onTouchStart = (e) => {
    const t = e.touches[0];
    if (t.clientX > 28 || sidebar) return;
    startRef.current = { x: t.clientX, y: t.clientY };
  };
  const onTouchMove = (e) => {
    if (!startRef.current) return;
    const t = e.touches[0];
    const dx = t.clientX - startRef.current.x;
    const dy = Math.abs(t.clientY - startRef.current.y);
    if (dy > Math.abs(dx)) { startRef.current = null; setDrag(0); return; }
    setDrag(Math.max(0, Math.min(dx, 280)));
  };
  const onTouchEnd = () => {
    if (!startRef.current) return;
    if (drag > 90) setSidebar(true);
    startRef.current = null;
    setDrag(0);
  };

  const peek = drag > 0 && !sidebar;

  return (
    <div class="zl-phone-wrap">
      <div class="zl-density-label">{label}</div>
      <div
        class="zl-phone"
        onTouchStart={onTouchStart}
        onTouchMove={onTouchMove}
        onTouchEnd={onTouchEnd}
      >
        <Transcript />

        {/* three floating capsules: sidebar / this session / new */}
        <div class="zl-chrome">
          <button type="button" class="zl-cap zl-cap-left" onClick={() => setSidebar(true)} aria-label="Sessions">
            <span class="zl-burger" aria-hidden="true" />
            <span class="zl-cap-badge" />
          </button>
          <button type="button" class="zl-cap zl-chip" onClick={() => setPanel(true)}>
            <span class="zl-chip-name">Buscar un bug bounty</span>
            <span class="zl-chev" aria-hidden="true">▾</span>
          </button>
          <button type="button" class="zl-cap zl-cap-right" aria-label="New session">+</button>
        </div>

        <div class="zl-composer">
          <span class="zl-plus">+</span>
          <span class="zl-ph">Message moa…</span>
          <span class="zl-send">↑</span>
        </div>
        <StatusLine />

        {(sidebar || peek) && (
          <div
            class="zl-scrim"
            style={peek ? `opacity:${Math.min(drag / 280, 1) * 0.6}` : ""}
            onClick={() => setSidebar(false)}
          />
        )}
        <div
          class={`zl-sidebar${sidebar ? " is-open" : ""}`}
          style={peek ? `transform:translateX(${drag - 280}px);transition:none` : ""}
        >
          <div class="zl-side-head">
            <span class="zl-side-title">moa</span>
            <button type="button" class="zl-side-new">New</button>
          </div>
          <div class="zl-side-search">Search sessions…</div>
          <SessionList onPick={() => setSidebar(false)} />
          <div class="zl-side-foot">
            <span>Inbox</span>
            <span class="zl-ver">v0.37.2</span>
          </div>
        </div>

        {panel && <div class="zl-scrim" onClick={() => setPanel(false)} />}
        {panel && <SessionPanel onClose={() => setPanel(false)} />}
      </div>
      <p class="zl-hint">
        Swipe from the left edge, or tap ≡. Tap the name to open the session panel.
      </p>
    </div>
  );
}

/* ── Desktop ───────────────────────────────────────────────────────────── */
function Desktop({ label }) {
  const [panel, setPanel] = useState(false);
  return (
    <div class="zl-desk-wrap">
      <div class="zl-density-label">{label}</div>
      <div class="zl-desk">
        <div class="zl-desk-side">
          <div class="zl-side-head">
            <span class="zl-side-title">moa</span>
            <button type="button" class="zl-side-new">New</button>
          </div>
          <div class="zl-side-search">Search ⌘K</div>
          <SessionList onPick={() => {}} />
          <div class="zl-side-foot">
            <span>Inbox</span>
            <span class="zl-ver">v0.37.2</span>
          </div>
        </div>
        <div class="zl-desk-main">
          <div class="zl-desk-head">
            <button type="button" class="zl-crumb" onClick={() => setPanel(true)}>
              <span class="zl-crumb-title">Buscar un bug bounty</span>
              <span class="zl-crumb-path">~/dev</span>
            </button>
            <span class="zl-spacer" />
            <span class="zl-desk-act" />
            <span class="zl-desk-act" />
          </div>
          <Transcript />
          <div class="zl-composer">
            <span class="zl-plus">+</span>
            <span class="zl-ph">Message moa…</span>
            <span class="zl-send">↑</span>
          </div>
          <StatusLine />
          {panel && <div class="zl-scrim" onClick={() => setPanel(false)} />}
          {panel && <SessionPanel onClose={() => setPanel(false)} />}
        </div>
      </div>
      <p class="zl-hint">
        The same list, permanent. The same session panel, opened from the crumb.
      </p>
    </div>
  );
}

export function ZonesLab() {
  useEffect(() => {
    document.documentElement.setAttribute("data-ambient", "on");
    return () => document.documentElement.removeAttribute("data-ambient");
  }, []);
  return (
    <div class="zl">
      <div class="zl-aurora" aria-hidden="true" />
      <header class="zl-head">
        <h1>Three zones</h1>
        <p>
          Left is the other sessions. Top-centre is this session. Bottom is
          state. The two densities differ in host — a slide-over against a
          permanent column — and share the list, the status line and the panel.
        </p>
      </header>
      <div class="zl-stage">
        <Phone label="Phone" />
        <Desktop label="Desktop" />
      </div>
    </div>
  );
}
