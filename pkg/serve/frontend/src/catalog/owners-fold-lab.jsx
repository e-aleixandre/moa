import { createPortal } from "preact/compat";
import { useEffect, useLayoutEffect, useState } from "preact/hooks";
import { ChevronRight } from "lucide-preact";
import { DesktopShell, ConversationScreen, MobileConversationScreen } from "../layout/index.js";
import { ScreenLab, DESKTOP_LAB_WIDTH, DESKTOP_LAB_HEIGHT, PHONE_LAB_WIDTH, PHONE_LAB_HEIGHT } from "./desktop-lab.jsx";
import { OwnerRow, SectionHead } from "../components/Owners/OwnerRow.jsx";
import { OwnerAvatarFor } from "../components/Owners/OwnerAvatar.jsx";
import { SessionCardMenu } from "../components/SessionCardMenu/SessionCardMenu.jsx";
import { ownerRows, ownerState, childrenSummary } from "../data/owners-model.js";
import { setState, store } from "../data/store.js";
import { setTileSession } from "../data/tileTree.js";
import { openDrawer } from "../data/drawer.js";
import { getToasts, removeToast, subscribeToasts } from "../data/notifications.js";
import "./owners-fold-lab.css";

/* Too many owners in the sidebar (?view=ownersfold) — the decided model.

   The owner closes an owner the way he closes a session: the SAME ⋯ menu
   (SessionCardMenu), the same gesture on both densities. A closed owner is
   unloaded and drops into one grouped row under the column ("7 closed ·
   1 working"). It rises on its own when it needs you (it asks, it wrote to
   you, a child waits on you, an event lands on it) and comes back for good
   when you open it. Working alone does not raise it. No automatic folding,
   no pinning.

   "Closed" is a flag the owner sets, not the runtime's saved state: after a
   restart every owner is saved, and none of them is closed (letmoa.run here).

   The chrome is production: DesktopShell and the real drawer. The shipped
   OWNERS block is hidden by a lab-scoped rule and this one is portalled in its
   place, built from OwnerRow, SectionHead, OwnerAvatar and SessionCardMenu.
   Nothing in production is edited. */

const now = Date.now();
const MIN = 60000;
const DAY = 86400000;
const ago = (ms) => now - ms;

// ── Owners: eleven, one per project, as the owner has them ──────────────

const O = (id, name, repo, avatar, lastUsedDays, extra = {}) => ({
  id,
  name,
  codebase_key: repo,
  root: `/home/ealeixandre/dev/${repo}/main`,
  session_id: `own-sess-${id}`,
  model: "anthropic/claude-opus-5-5",
  thinking: "low",
  answer_asks: true,
  created: now - 60 * DAY,
  session_state: "saved",
  avatar: avatar,
  lastUsed: ago(lastUsedDays * DAY),
  ...extra,
});

const OWNERS = [
  O("moa", "moa", "moa", { shape: "squircle", color: "mauve" }, 0, { session_state: "running", lastUsed: ago(2 * MIN), ownReason: "Reading 2 reports" }),
  O("winerim", "Winerim", "winerim-backend", { shape: "circle", color: "rose" }, 0, { session_state: "idle", lastUsed: ago(4 * MIN) }),
  O("ourown", "Ourown Studio", "ourown-studio", { shape: "blob", color: "sage" }, 0, { session_state: "idle", lastUsed: ago(2 * 60 * MIN) }),
  // Saved by the last restart, NOT closed by the owner: it stays in the column.
  O("landing", "letmoa.run", "letmoa-landing", { shape: "pill", color: "sky" }, 2),
  O("pulse", "Pulse", "moa-companion-ios", { shape: "drop", color: "azure" }, 6),
  O("kiru", "Kiru", "kiru-api", { shape: "hexagon", color: "peach" }, 5),
  O("dotfiles", "dotfiles", "dotfiles", { shape: "circle", color: "lilac", tone: "dark" }, 9),
  // Closed, but a scheduled job woke it: it stays in the closed group, which
  // reports it as "1 working".
  O("torres", "Bodegas Torres", "torres-portal", { shape: "cloud", color: "peach", tone: "dark" }, 12, {
    session_state: "running", ownReason: "Weekly stock sync · 3 of 7 warehouses",
  }),
  O("albeniz", "Clínica Albéniz", "albeniz-citas", { shape: "hexagon", color: "mint" }, 19),
  O("lavinia", "Lavinia", "lavinia-shop", { shape: "diamond", color: "rose", tone: "dark" }, 27),
  O("sanz", "Ferretería Sanz", "sanz-tpv", { shape: "shield", color: "sage", tone: "dark" }, 34),
];

const OWNER_REASON = Object.fromEntries(OWNERS.filter((o) => o.ownReason).map((o) => [o.id, o.ownReason]));
const LAST_USED = Object.fromEntries(OWNERS.map((o) => [o.id, o.lastUsed]));

// ── Sessions: the children that give the busy owners their words ────────

function session(id, title, repo, ownerId, state, agoMin, extra = {}) {
  return {
    id, title, cwd: `/home/ealeixandre/dev/${repo}/main`, state, ownerId, updated: ago(agoMin * MIN),
    model: "Claude Opus 5.5", provider: "anthropic", thinking: "medium",
    permissionMode: "yolo", contextPercent: 34, contextWindow: 200000,
    costUSD: 2.1, runTokensUp: 48000, runTokensDown: 6100,
    messages: [], subagents: {},
    ...(state === "running" ? { runStartedAtMs: ago((agoMin + 2) * MIN), briefProgress: "Working" } : null),
    ...extra,
  };
}

const say = (text, m) => ({ role: "assistant", timestamp: ago(m * MIN), content: [{ type: "text", text }] });
const user = (id, text, m) => ({ msg_id: id, role: "user", timestamp: ago(m * MIN), content: [{ type: "text", text }] });

function sessions() {
  const list = [
    session("race", "Carrera en el borrado de adjuntos", "moa", "moa", "running", 1, {
      briefProgress: "go test -race ./pkg/attach",
      messages: [
        user("u1", "Reproduce la carrera del borrado de adjuntos con 100 borrados en paralelo y arréglala.", 30),
        say("Reproducido: 3 de 100 borrados dejan el blob huérfano. Muevo la comprobación dentro del lock y repito con `-race`.", 2),
      ],
    }),
    session("torres-alb", "Importar albaranes de Bodegas Torres", "winerim-backend", "winerim", "permission", 4, { permissionTool: "bash" }),
    session("tarifas", "Migrar las tarifas de distribuidor", "winerim-backend", "winerim", "running", 9, { briefProgress: "go test ./internal/tarifas/..." }),
    session("checkout", "Checkout con Stripe", "ourown-studio", "ourown", "idle", 120),
    session("hero", "Hero de la landing con el vídeo nuevo", "letmoa-landing", "landing", "saved", 2 * 24 * 60),
    session("apns", "Push nativo con APNs", "moa-companion-ios", "pulse", "saved", 6 * 24 * 60),
  ];
  return Object.fromEntries(list.map((s) => [s.id, s]));
}

// ── Lab state (one per page) ─────────────────────────────────────────────

const params = new URLSearchParams(typeof location === "undefined" ? "" : location.search);
const lab = {
  get v() { return params.get("v") === "now" ? "now" : "close"; },
  get device() { return params.get("device") || "desktop"; },
  get scene() { return params.get("scene") || "rest"; },
};

const CLOSED = ["pulse", "kiru", "dotfiles", "torres", "albeniz", "lavinia", "sanz"];

let ui = {
  closed: [...CLOSED], // the persisted flag: set by Close, cleared by opening
  foldOpen: false,
  event: null, // owner id an event just woke
  active: "moa",
};
const listeners = new Set();
const setUI = (patch) => {
  ui = { ...ui, ...(typeof patch === "function" ? patch(ui) : patch) };
  listeners.forEach((fn) => fn());
};
function useUI() {
  const [, force] = useState(0);
  useEffect(() => { const fn = () => force((n) => n + 1); listeners.add(fn); return () => listeners.delete(fn); }, []);
  return ui;
}
function useOwners() {
  const [, force] = useState(0);
  useEffect(() => store.subscribe(() => force((n) => n + 1)), []);
  const s = store.get();
  return ownerRows(s.owners.list, s.sessions).map((o) => ({
    ...o,
    ownReason: o.ownReason || OWNER_REASON[o.id] || "",
    lastUsed: LAST_USED[o.id],
  }));
}

// ── The rule ─────────────────────────────────────────────────────────────

// needsYou — what brings a closed owner up on its own: its own question or
// error, something it wrote you have not read, a child stopped on you, or an
// event that just landed on it. Working alone does not.
function needsYou(o, u) {
  const st = ownerState(o);
  if (st === "asks" || st === "unread") return true;
  if (childrenSummary(o.children).waiting > 0) return true;
  return u.event === o.id;
}

function isWorking(o) {
  return ownerState(o) === "working" || childrenSummary(o.children).working > 0;
}

// The column keeps the owners' own order; a closed owner that needs you takes
// its usual place in it rather than jumping to the top.
function split(owners, u) {
  const closed = new Set(u.closed);
  return {
    top: owners.filter((o) => !closed.has(o.id) || needsYou(o, u)),
    folded: owners.filter((o) => closed.has(o.id) && !needsYou(o, u)),
  };
}

// The closed group said in words, as the owner row says them
// (decisions/lenguaje-de-estado.md): only what moves, nothing when all rest.
function foldSummary(folded) {
  const working = folded.filter(isWorking).length;
  return working > 0 ? `${working} working` : "";
}

function age(ms) {
  const d = Math.floor((now - ms) / DAY);
  if (d >= 1) return `${d}d`;
  const h = Math.floor((now - ms) / (60 * MIN));
  return h >= 1 ? `${h}h` : "now";
}

// ── Actions ──────────────────────────────────────────────────────────────

function setOwnerState(id, state, patch = {}) {
  setState((s) => ({
    owners: { ...s.owners, list: s.owners.list.map((x) => (x.id === id ? { ...x, session_state: state, ...patch } : x)) },
  }));
}
const byId = (id) => store.get().owners.list.find((o) => o.id === id);

// Opening is what brings a closed owner back: the flag goes, the conversation
// loads if it was on disk.
function openOwner(o) {
  if (ownerState(o) === "saved") setOwnerState(o.id, "idle");
  setUI((u) => ({ active: o.id, closed: u.closed.filter((x) => x !== o.id) }));
}
// Close = Close session on the owner's conversation (unload, eyes shut) + the
// flag. The server refuses mid-run (409) and the menu's existing toast says so,
// exactly as for a session; the lab mutes toasts.
function closeOwner(id) {
  if (ownerState(byId(id)) === "working") return;
  setOwnerState(id, "saved");
  setUI((u) => ({ closed: [...new Set([...u.closed, id])], active: u.active === id ? null : u.active }));
}
function reopenOwner(id) { openOwner(byId(id)); }

// ── Rows: the owner row + the session list's own ⋯ ───────────────────────

// Same wrapper, same menu, same props as Sidebar.jsx row(): `.zl-session
// is-menu` + SessionCardMenu. `saved` is the closed flag, so the menu offers
// Reopen on a closed owner even while a report has it awake.
function Menu({ o, u }) {
  return (
    <SessionCardMenu
      session={{ id: o.session_id, saved: u.closed.includes(o.id) }}
      onClose={() => closeOwner(o.id)}
      onReopen={() => reopenOwner(o.id)}
      onDelete={() => {}}
      scrollContainerSelector=".zl-list"
    />
  );
}

function TopRow({ o, u }) {
  return (
    <div class="zl-session is-menu ofl-omenu" data-owner={o.id}>
      <OwnerRow owner={o} active={o.id === u.active} onOpen={openOwner} />
      <Menu o={o} u={u} />
    </div>
  );
}

function FoldedRow({ o, phone, u }) {
  const working = isWorking(o);
  return (
    <div class="zl-session is-menu ofl-omenu ofl-cslot" data-owner={o.id}>
      <span class="zl-row-slot">
        <button
          type="button"
          class={`zl-row ofl-crow${o.id === u.active ? " is-current" : ""}`}
          onClick={() => openOwner(o)}
          aria-label={`${o.name}, project owner, closed${working ? ", working" : ""}`}
        >
          <OwnerAvatarFor owner={o} state={ownerState(o)} size={phone ? 24 : 20} />
          <span class="ofl-crow-name">{o.name}</span>
          {working
            ? <span class="ofl-crow-state zl-row-when">working</span>
            : <span class="ofl-crow-age zl-row-when zl-data">{age(o.lastUsed)}</span>}
        </button>
      </span>
      <Menu o={o} u={u} />
    </div>
  );
}

function FoldHead({ folded, open, onToggle, phone }) {
  const summary = foldSummary(folded);
  return (
    <button
      type="button"
      class={`ofl-fold${open ? " is-open" : ""}${phone ? " is-phone" : ""}`}
      aria-expanded={open}
      aria-label={`${folded.length} closed${summary ? `, ${summary}` : ""}`}
      onClick={onToggle}
    >
      <span class="ofl-stack" aria-hidden="true">
        {folded.slice(0, 3).map((o) => (
          <span class="ofl-stack-face" key={o.id}><OwnerAvatarFor owner={o} state="saved" size={phone ? 20 : 18} /></span>
        ))}
      </span>
      <span class="ofl-fold-label"><span class="ofl-fold-n">{folded.length}</span> closed</span>
      {summary && <span class="ofl-fold-sum">{summary}</span>}
      <ChevronRight class="ofl-fold-chev" size={14} aria-hidden="true" />
    </button>
  );
}

function OwnersBlock({ phone }) {
  const u = useUI();
  const owners = useOwners();
  const [open, setOpen] = useState(true);
  const { top, folded } = split(owners, u);
  return (
    <div class={`ofl-block${phone ? " is-phone" : ""}`}>
      <SectionHead label="Owners" n={owners.length} open={open} dot={null} onToggle={() => setOpen(!open)} />
      {open && (
        <>
          {top.map((o) => <TopRow key={o.id} o={o} u={u} />)}
          {folded.length > 0 && (
            <>
              <FoldHead folded={folded} open={u.foldOpen} onToggle={() => setUI({ foldOpen: !u.foldOpen })} phone={phone} />
              {u.foldOpen && (
                <div class="ofl-folded">
                  {folded.map((o) => <FoldedRow key={o.id} o={o} phone={phone} u={u} />)}
                </div>
              )}
            </>
          )}
          <button type="button" class="ow-newowner ofl-newowner">
            <span aria-hidden="true">+</span>
            New owner
          </button>
        </>
      )}
    </div>
  );
}

// ── Mounting into the real chrome ────────────────────────────────────────

function usePortalHost(selector, { prepend = false } = {}) {
  const [host] = useState(() => {
    const el = document.createElement("div");
    el.style.display = "contents";
    el.className = "ofl-portal";
    return el;
  });
  const [target, setTarget] = useState(null);
  useLayoutEffect(() => {
    const place = () => {
      const t = document.querySelector(selector);
      if (!t) { if (host.parentNode) host.remove(); setTarget(null); return; }
      if (host.parentNode !== t || (prepend && t.firstChild !== host)) {
        t.insertBefore(host, prepend ? t.firstChild : null);
      }
      setTarget(t);
    };
    place();
    const mo = new MutationObserver(place);
    mo.observe(document.querySelector(".ofl-stage") || document.body, { childList: true, subtree: true });
    return () => { mo.disconnect(); host.remove(); };
  }, [selector]);
  return target ? host : null;
}

function OwnersInSidebar({ phone }) {
  const host = usePortalHost(".ofl-stage .zl-list:not(.is-inbox)", { prepend: true });
  return host ? createPortal(<OwnersBlock phone={phone} />, host) : null;
}

function DesktopStage() {
  return (
    <DesktopShell>
      <ConversationScreen />
      {lab.v !== "now" && <OwnersInSidebar />}
    </DesktopShell>
  );
}

function PhoneStage() {
  useEffect(() => { openDrawer("list"); }, []);
  return (
    <>
      <MobileConversationScreen forceMobile />
      {lab.v !== "now" && <OwnersInSidebar phone />}
    </>
  );
}

// ── Seed, scenes, lab bar ────────────────────────────────────────────────

function seed() {
  const scene = lab.scene;
  ui = {
    ...ui,
    closed: scene === "closing" ? [...CLOSED, "landing"] : [...CLOSED],
    foldOpen: ["open", "closing", "reading", "menu-closed"].includes(scene),
    event: scene === "event" ? "albeniz" : null,
  };
  const owners = OWNERS.map((o) => {
    if (scene === "event" && o.id === "albeniz") return { ...o, session_state: "running" };
    if (scene === "reading" && o.id === "pulse") return { ...o, session_state: "running" };
    if (scene === "wrote" && o.id === "pulse") return { ...o, session_state: "idle", unseen: true };
    return o;
  });
  if (scene === "event") OWNER_REASON.albeniz = "Email from Laura Gil · reading";
  if (scene === "reading") OWNER_REASON.pulse = "Reading a report · Push nativo con APNs";
  if (scene === "wrote") OWNER_REASON.pulse = "APNs works in staging · wrote to you";
  setState((s) => ({
    sessions: sessions(),
    sessionsLoaded: true,
    activeSession: "race",
    events: [],
    sessionPanel: { open: false, sessionId: null, page: "root" },
    drawerOpen: false,
    inboxOpen: false,
    sidebarMode: "recent",
    owners: { ...s.owners, list: owners, loaded: true },
    tileTree: setTileSession(s.tileTree, s.focusedTile, "race"),
  }));
}

// Scenes that open a ⋯ menu do it with a real click on the real trigger.
const MENU_ON = { menu: "landing", "menu-closed": "kiru" };
function useSceneMenu() {
  useEffect(() => {
    const id = MENU_ON[lab.scene];
    if (!id) return undefined;
    const t = setTimeout(() => {
      document.querySelector(`.ofl-stage [data-owner="${id}"] .session-card-menu-button`)?.click();
    }, 900);
    return () => clearTimeout(t);
  }, []);
}

function useNoToasts() {
  useEffect(() => {
    const clear = () => getToasts().forEach((t) => removeToast(t.id));
    clear();
    return subscribeToasts(() => setTimeout(clear, 0));
  }, []);
}

const VARIANTS = [["now", "Today"], ["close", "Close owner (decided)"]];
const DEVICES = [["desktop", "Desktop"], ["phone", "Phone"]];
const SCENES = [
  ["rest", "At rest"], ["open", "Closed open"], ["menu", "⋯ on an owner"], ["closing", "letmoa closed"],
  ["menu-closed", "⋯ on a closed owner"], ["event", "Client email arrives"],
  ["reading", "Closed Pulse gets a report"], ["wrote", "…and writes to you"],
];

// Live: a client's email lands on a closed owner, or the owner is done with it.
function toggleEmail() {
  const on = ui.event !== "albeniz";
  if (on) OWNER_REASON.albeniz = "Email from Laura Gil · reading";
  else delete OWNER_REASON.albeniz;
  setOwnerState("albeniz", on ? "running" : "saved");
  setUI({ event: on ? "albeniz" : null });
}

// Live: a child reports to Pulse, which is closed. The server resumes it
// (reports.go deliverReportsIfIdle): it reads inside the group, then writes to
// you and rises.
function reportToPulse() {
  OWNER_REASON.pulse = "Reading a report · Push nativo con APNs";
  setOwnerState("pulse", "running");
  setTimeout(() => {
    OWNER_REASON.pulse = "APNs works in staging · wrote to you";
    setOwnerState("pulse", "idle", { unseen: true });
  }, 3000);
}

function LiveButtons() {
  const u = useUI();
  return (
    <div class="ofl-lab-seg">
      <button type="button" class={`ofl-lab-opt ofl-lab-btn${u.event ? " is-on" : ""}`} onClick={toggleEmail}>
        {u.event ? "Albéniz: done with the email" : "Live: email to Clínica Albéniz"}
      </button>
      <button type="button" class="ofl-lab-opt ofl-lab-btn" onClick={reportToPulse}>Live: report to closed Pulse</button>
    </div>
  );
}

function LabBar() {
  const href = (p) => {
    const q = new URLSearchParams(location.search);
    Object.entries(p).forEach(([k, v]) => q.set(k, v));
    return `?${q.toString()}`;
  };
  const Seg = ({ items, cur, k }) => (
    <div class="ofl-lab-seg">
      {items.map(([id, label]) => <a key={id} href={href({ [k]: id })} class={`ofl-lab-opt${cur === id ? " is-on" : ""}`}>{label}</a>)}
    </div>
  );
  return (
    <div class="ofl-lab-bar">
      <Seg k="v" cur={lab.v} items={VARIANTS} />
      <Seg k="device" cur={lab.device} items={DEVICES} />
      {lab.v !== "now" && <Seg k="scene" cur={lab.scene} items={SCENES} />}
      {lab.v !== "now" && <LiveButtons />}
    </div>
  );
}

export function OwnersFoldLab() {
  const [ready, setReady] = useState(false);
  useNoToasts();
  useSceneMenu();
  useLayoutEffect(() => { seed(); setReady(true); }, []);
  const phone = lab.device === "phone";
  return (
    <div class={`ofl-lab${lab.v === "now" ? " is-today" : ""}`}>
      <LabBar />
      <div class="ofl-stage">
        {ready && (
          <ScreenLab width={phone ? PHONE_LAB_WIDTH : DESKTOP_LAB_WIDTH} height={phone ? PHONE_LAB_HEIGHT : DESKTOP_LAB_HEIGHT} note={null}>
            {phone ? <PhoneStage /> : <DesktopStage />}
          </ScreenLab>
        )}
      </div>
    </div>
  );
}
