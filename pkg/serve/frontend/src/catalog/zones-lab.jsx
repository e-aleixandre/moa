import { useEffect, useRef, useState } from "preact/hooks";
import "./zones-lab.css";

/* The three-zone skeleton, both densities side by side.
   This is a PROTOTYPE, not production: it draws the shell only (where things
   live and how they open), with the real transcript stubbed as grey bars.
   Spatial grammar: the LEFT edge is the other sessions, the RIGHT edge is
   this session, the bottom is state. Both drawers use the same motion and the
   same gesture, mirrored, so learning one teaches the other. */

/* Every session carries the project it lives in, as a coloured monogram. The
   references all put an icon on every row; a chat client has no icon per
   conversation, but it does have a folder -- and that is the thing you
   actually navigate by, so it earns the slot. Colour is derived from the
   project name, so the same repo always looks the same. */
const SESSIONS = [
  { title: "Buscar un bug bounty", when: "now", path: "~/dev/moa", project: "moa", state: "running", brief: "Running · 4m" },
  { title: "Check access to two repos", when: "28m", path: "~/dev/gugo", project: "gugo", state: "needs", brief: "Needs your answer" },
  { title: "Deploy fails on ARM runner", when: "1h", path: "~/dev/tienda", project: "tienda", state: "error", brief: "Stopped with an error" },
  { title: "Limpiar Docker y worktrees", when: "35m", path: "~/dev", project: "dev", state: "idle" },
  { title: "Búscame un dominio para el side project", when: "36m", path: "~/dev", project: "dev", state: "idle" },
  { title: "Browse Gugo GitLab", when: "39d", path: "~/dev/gugo", project: "gugo", state: "idle" },
  { title: "MenuApp", when: "41d", path: "~/dev/menuapp", project: "menuapp", state: "idle" },
];

/* Identity hues, deliberately none of them peach: that one means "you wrote
   this" and may not be spent on decoration. */
const HUES = [210, 265, 170, 320, 40, 190];
function projectHue(name) {
  let h = 0;
  for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) >>> 0;
  return HUES[h % HUES.length];
}

function Monogram({ project, state }) {
  const hue = projectHue(project);
  return (
    <span
      class={`zl-mono is-${state}`}
      style={`--h:${hue}`}
      aria-hidden="true"
    >
      {project.slice(0, 2)}
    </span>
  );
}

const ACTIVE = SESSIONS.filter((s) => s.state !== "idle");
const SAVED = SESSIONS.filter((s) => s.state === "idle");

function Dot({ state }) {
  return <span class={`zl-dot is-${state}`} aria-hidden="true" />;
}

function Row({ s, current, onPick }) {
  return (
    <button
      type="button"
      class={`zl-row${current ? " is-current" : ""}`}
      aria-current={current ? "true" : undefined}
      onClick={onPick}
    >
      <Monogram project={s.project} state={s.state} />
      <span class="zl-row-main">
        <span class="zl-row-l1">
          <span class="zl-row-title">{s.title}</span>
          <span class="zl-row-when">{s.when}</span>
        </span>
        {/* Active sessions say what they are doing; saved ones say where they
            live. Two lines is the budget, so the more useful datum wins. */}
        {s.brief
          ? <span class={`zl-row-brief is-${s.state}`}>{s.brief}</span>
          : <span class="zl-row-path">{s.path}</span>}
      </span>
    </button>
  );
}

function SessionList({ onPick }) {
  return (
    <div class="zl-list">
      <div class="zl-group"><span>Active</span><span class="zl-group-n">{ACTIVE.length}</span></div>
      {ACTIVE.map((s, i) => <Row s={s} current={i === 0} onPick={onPick} key={s.title} />)}
      <div class="zl-group"><span>Saved</span><span class="zl-group-n">{SAVED.length}</span></div>
      {SAVED.map((s) => <Row s={s} current={false} onPick={onPick} key={s.title} />)}
    </div>
  );
}

/* The left drawer body, shared by both densities. */
function Sidebar({ onPick, desktop }) {
  return (
    <>
      <div class="zl-side-head">
        <span class="zl-side-title">moa</span>
        {/* Search is the quietest thing here, not the heaviest: a line with an
            icon, no filled box competing with the sessions it searches. */}
        <label class="zl-search">
          <svg class="zl-search-ico" viewBox="0 0 16 16" aria-hidden="true">
            <circle cx="7" cy="7" r="4.5" fill="none" stroke="currentColor" stroke-width="1.6" />
            <path d="M10.5 10.5L14 14" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" />
          </svg>
          <input class="zl-search-in" placeholder="Search" aria-label="Search sessions" />
          {desktop && <kbd class="zl-kbd">⌘K</kbd>}
        </label>
      </div>
      <SessionList onPick={onPick} />
      {/* New anchors the bottom, where the thumb is and where the empty half of
          the column was. It is the one action, so it gets the width. */}
      <button type="button" class="zl-side-new">
        <span class="zl-side-new-plus" aria-hidden="true">+</span>New session
      </button>
      <div class="zl-side-foot">
        <button type="button" class="zl-inbox">
          <svg viewBox="0 0 16 16" aria-hidden="true">
            <path d="M2 9.5V12a1 1 0 0 0 1 1h10a1 1 0 0 0 1-1V9.5M2 9.5h3.2l.8 1.5h4l.8-1.5H14M2 9.5l1.6-5.2A1 1 0 0 1 4.6 3.5h6.8a1 1 0 0 1 1 .8L14 9.5" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linejoin="round" />
          </svg>
          Inbox
          <span class="zl-inbox-n">1</span>
        </button>
        <span class="zl-ver">v0.37.2</span>
      </div>
    </>
  );
}

/* The right drawer: this session. Deliberately excludes model, permissions
   and fast -- those live in the status line, and putting them here too would
   break "one datum, one place". */
function SessionPanel({ onClose }) {
  return (
    <>
      <div class="zl-side-head">
        <span class="zl-side-title is-eyebrow">This session</span>
        <button type="button" class="zl-x" onClick={onClose} aria-label="Close">
          <svg viewBox="0 0 16 16" aria-hidden="true">
            <path d="M4 4l8 8M12 4l-8 8" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" />
          </svg>
        </button>
      </div>
      <div class="zl-panel-body">
        <label class="zl-field">
          <span class="zl-label">Name</span>
          <input class="zl-input" defaultValue="Buscar un bug bounty" />
        </label>
        <div class="zl-field">
          <span class="zl-label">Folder</span>
          <div class="zl-input is-static">
            <span class="zl-path-dir">~/dev/moa/</span>main
          </div>
        </div>
        <dl class="zl-facts">
          <div><dt>Started</dt><dd>Today, 09:12</dd></div>
          <div><dt>Turns</dt><dd>14</dd></div>
          <div><dt>Branch</dt><dd>design-visual</dd></div>
        </dl>
      </div>
      <div class="zl-panel-acts">
        <button type="button" class="zl-act">
          <span class="zl-act-t">Save for later</span>
          <span class="zl-act-d">Stops the agent, keeps the session in Saved.</span>
        </button>
        <button type="button" class="zl-act is-danger">
          <span class="zl-act-t">Close session</span>
          <span class="zl-act-d">Removes it from the list. The transcript stays on disk.</span>
        </button>
      </div>
    </>
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

/* Context ring: a real arc, coloured by how close to the wall the session is.
   The colour is semantic (fine / getting full / compact soon), not decor. */
function CtxRing({ pct }) {
  const r = 6.5;
  const c = 2 * Math.PI * r;
  const tone = pct >= 90 ? "is-hot" : pct >= 70 ? "is-warm" : "";
  return (
    <svg class={`zl-ring ${tone}`} viewBox="0 0 16 16" aria-hidden="true">
      <circle cx="8" cy="8" r={r} class="zl-ring-track" />
      <circle
        cx="8" cy="8" r={r} class="zl-ring-arc"
        stroke-dasharray={`${(c * pct) / 100} ${c}`}
        transform="rotate(-90 8 8)"
      />
    </svg>
  );
}

/* Status line. Two clusters, one datum each:
   LEFT  = what this session is set to (model, permissions) -- controls.
   RIGHT = what this run is doing to the budget (context, tokens) -- readings.
   Controls are buttons because in the product they open pickers; readings are
   text because there is nothing to change. */
function StatusLine() {
  return (
    <div class="zl-status">
      <button type="button" class="zl-st zl-st-model" aria-label="Model & thinking: Daybreak Blue, medium">
        <span class="zl-st-model-name">Daybreak Blue</span>
        <span class="zl-think" aria-hidden="true"><i /><i /><i class="is-off" /></span>
      </button>
      <button type="button" class="zl-st zl-st-perm is-yolo" aria-label="Permission mode: yolo">
        yolo
      </button>
      <span class="zl-spacer" />
      <span class="zl-st zl-st-ctx" title="Context used">
        <CtxRing pct={10} />
        <span class="zl-num">10<span class="zl-unit">%</span></span>
      </span>
      <span class="zl-st zl-st-tok" title="Tokens this run">
        <span class="zl-arrow" aria-hidden="true">↑</span><span class="zl-num">12.4k</span>
        <span class="zl-arrow" aria-hidden="true">↓</span><span class="zl-num">1.8k</span>
      </span>
    </div>
  );
}

/* Composer. A sunken well, not a pill: the field is the thing you look at
   most, so it gets the most careful surface. The send button arms when there
   is something to send and stays achromatic -- peach is "you said this",
   which is what the message becomes AFTER sending, not the button. */
function Composer() {
  const [draft, setDraft] = useState("");
  const ref = useRef(null);
  const onInput = (e) => {
    setDraft(e.currentTarget.value);
    const el = e.currentTarget;
    el.style.height = "0";
    el.style.height = `${Math.min(el.scrollHeight, 132)}px`;
  };
  const armed = draft.trim().length > 0;
  return (
    <div class={`zl-composer${armed ? " is-armed" : ""}`}>
      <button type="button" class="zl-attach" aria-label="Attach">
        <svg viewBox="0 0 16 16" aria-hidden="true">
          <path d="M8 3.5v9M3.5 8h9" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" />
        </svg>
      </button>
      <textarea
        ref={ref}
        class="zl-ta"
        rows="1"
        placeholder="Message moa"
        value={draft}
        onInput={onInput}
        aria-label="Message"
      />
      <button type="button" class="zl-send" aria-label="Send" disabled={!armed}>
        <svg viewBox="0 0 16 16" aria-hidden="true">
          <path d="M8 13V3.5M8 3.5L3.8 7.7M8 3.5l4.2 4.2" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" />
        </svg>
      </button>
    </div>
  );
}

/* Edge gestures, mirrored. A 28px zone on either edge starts a drag that
   pulls that edge's drawer in; the drawer follows the finger and commits past
   90px. With a drawer open, dragging it back toward its own edge closes it.
   The vertical guard abandons the gesture if the finger is really scrolling. */
const W_LEFT = 300;
const W_RIGHT = 320;
const EDGE = 28;

function useEdgeDrawers(hostRef) {
  const [left, setLeft] = useState(false);
  const [right, setRight] = useState(false);
  const [drag, setDrag] = useState(null); // { side, dx }
  const start = useRef(null);

  const onTouchStart = (e) => {
    const host = hostRef.current.getBoundingClientRect();
    const t = e.touches[0];
    const x = t.clientX - host.left;
    let side = null;
    if (left) side = "left";
    else if (right) side = "right";
    else if (x <= EDGE) side = "left";
    else if (x >= host.width - EDGE) side = "right";
    if (!side) return;
    start.current = { x: t.clientX, y: t.clientY, side };
  };
  const onTouchMove = (e) => {
    if (!start.current) return;
    const t = e.touches[0];
    const dx = t.clientX - start.current.x;
    const dy = Math.abs(t.clientY - start.current.y);
    if (drag == null && dy > Math.abs(dx)) { start.current = null; return; }
    setDrag({ side: start.current.side, dx });
  };
  const onTouchEnd = () => {
    if (!start.current) { setDrag(null); return; }
    const { side } = start.current;
    const dx = drag?.dx || 0;
    if (side === "left") {
      if (left && dx < -90) setLeft(false);
      if (!left && dx > 90) setLeft(true);
    } else {
      if (right && dx > 90) setRight(false);
      if (!right && dx < -90) setRight(true);
    }
    start.current = null;
    setDrag(null);
  };

  // Offsets while dragging, clamped so a drawer never overshoots its edge.
  const leftX = drag?.side === "left"
    ? Math.max(-W_LEFT, Math.min(0, (left ? 0 : -W_LEFT) + drag.dx))
    : null;
  const rightX = drag?.side === "right"
    ? Math.max(0, Math.min(W_RIGHT, (right ? 0 : W_RIGHT) + drag.dx))
    : null;
  const veil = leftX != null
    ? 1 + leftX / W_LEFT
    : rightX != null
      ? 1 - rightX / W_RIGHT
      : null;

  return {
    left, right, setLeft, setRight, leftX, rightX, veil,
    handlers: { onTouchStart, onTouchMove, onTouchEnd },
  };
}

/* ── Phone ─────────────────────────────────────────────────────────────── */
function Phone({ label }) {
  const host = useRef(null);
  const d = useEdgeDrawers(host);
  const anyOpen = d.left || d.right || d.veil != null;

  return (
    <div class="zl-phone-wrap">
      <div class="zl-density-label">{label}</div>
      <div class="zl-phone" ref={host} {...d.handlers}>
        <Transcript />

        {/* three floating capsules: sidebar / this session / new */}
        <div class="zl-chrome">
          <button type="button" class="zl-cap zl-cap-left" onClick={() => d.setLeft(true)} aria-label="Sessions">
            <span class="zl-burger" aria-hidden="true" />
            <span class="zl-cap-badge" />
          </button>
          <button type="button" class="zl-cap zl-chip" onClick={() => d.setRight(true)}>
            <span class="zl-chip-name">Buscar un bug bounty</span>
            <span class="zl-chev" aria-hidden="true">▾</span>
          </button>
          <button type="button" class="zl-cap zl-cap-right" aria-label="New session">+</button>
        </div>

        <div class="zl-dock">
          <Composer />
          <StatusLine />
        </div>

        {anyOpen && (
          <div
            class="zl-scrim"
            style={d.veil != null ? `opacity:${d.veil};transition:none` : ""}
            onClick={() => { d.setLeft(false); d.setRight(false); }}
          />
        )}
        <div
          class={`zl-side zl-side-left${d.left ? " is-open" : ""}`}
          style={d.leftX != null ? `transform:translateX(${d.leftX}px);transition:none` : ""}
        >
          <Sidebar onPick={() => d.setLeft(false)} />
        </div>
        <div
          class={`zl-side zl-side-right${d.right ? " is-open" : ""}`}
          role="dialog"
          aria-label="This session"
          aria-hidden={!d.right}
          style={d.rightX != null ? `transform:translateX(${d.rightX}px);transition:none` : ""}
        >
          <SessionPanel onClose={() => d.setRight(false)} />
        </div>
      </div>
      <p class="zl-hint">
        Swipe in from the left edge for the other sessions, from the right edge
        for this one. Or tap ≡ and the name.
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
          <Sidebar onPick={() => {}} desktop />
        </div>
        <div class="zl-desk-main">
          <div class="zl-desk-head">
            <button type="button" class="zl-crumb" onClick={() => setPanel(true)} aria-expanded={panel}>
              <span class="zl-crumb-title">Buscar un bug bounty</span>
              <span class="zl-crumb-path">~/dev/moa</span>
            </button>
            <span class="zl-spacer" />
            <span class="zl-desk-act" />
            <span class="zl-desk-act" />
          </div>
          <Transcript />
          <div class="zl-dock">
            <Composer />
            <StatusLine />
          </div>
          {panel && <div class="zl-scrim" onClick={() => setPanel(false)} />}
          <div
            class={`zl-side zl-side-right${panel ? " is-open" : ""}`}
            role="dialog"
            aria-label="This session"
            aria-hidden={!panel}
          >
            <SessionPanel onClose={() => setPanel(false)} />
          </div>
        </div>
      </div>
      <p class="zl-hint">
        The same list, permanent, on the left. The same session drawer slides
        in over the transcript from the right, opened from the crumb.
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
          Left is the other sessions. Right is this session. Bottom is state.
          The two densities differ in host — drawers against a permanent column
          — and share the list, the dock and the session drawer.
        </p>
      </header>
      <div class="zl-stage">
        <Phone label="Phone" />
        <Desktop label="Desktop" />
      </div>
    </div>
  );
}
