import { createPortal } from "preact/compat";
import { useEffect, useLayoutEffect, useRef, useState } from "preact/hooks";
import { ChevronRight, Pin, PinOff, EyeOff, Eye, MessageSquare, Pencil } from "lucide-preact";
import { DesktopShell, ConversationScreen, MobileConversationScreen } from "../layout/index.js";
import { ScreenLab, DESKTOP_LAB_WIDTH, DESKTOP_LAB_HEIGHT, PHONE_LAB_WIDTH, PHONE_LAB_HEIGHT } from "./desktop-lab.jsx";
import { MobileSheet } from "../layout/mobile/MobileSheet/MobileSheet.jsx";
import { OwnerRow, SectionHead } from "../components/Owners/OwnerRow.jsx";
import { OwnerAvatarFor } from "../components/Owners/OwnerAvatar.jsx";
import { ownerRows, ownerState, childrenSummary } from "../data/owners-model.js";
import { setState, store } from "../data/store.js";
import { setTileSession } from "../data/tileTree.js";
import { openDrawer } from "../data/drawer.js";
import { getToasts, removeToast, subscribeToasts } from "../data/notifications.js";
import "./owners-fold-lab.css";

/* Too many owners in the sidebar (?view=ownersfold).

   The chrome is production: DesktopShell with its real Sidebar, and on the
   phone MobileConversationScreen with its real drawer (which mounts the same
   Sidebar). The production OWNERS block is hidden by a lab-scoped rule and the
   variant is portalled in its place, at the top of the same `.zl-list`, built
   from the shipped OwnerRow, SectionHead and OwnerAvatar. Nothing in
   production is edited.

   Three variants, one question each:
     A · Pinned + More  — you choose who stays (manual).
     B · Quiet fold     — the list chooses: N days without use folds (automatic,
                          pin overrides).
     C · Face shelf     — B's rule, but what is folded is a row of faces
                          instead of a closed list.
   In all three an owner that needs you (asks, wrote to you, a child waiting,
   an incoming event) is in the top list whatever the rule says. */

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
  O("landing", "letmoa.run", "letmoa-landing", { shape: "pill", color: "sky" }, 2),
  O("pulse", "Pulse", "moa-companion-ios", { shape: "drop", color: "azure" }, 6),
  O("kiru", "Kiru", "kiru-api", { shape: "hexagon", color: "peach" }, 5),
  O("dotfiles", "dotfiles", "dotfiles", { shape: "circle", color: "lilac", tone: "dark" }, 9),
  // A scheduled job keeps Torres busy while nobody has touched it in 12 days:
  // it is folded, and it is what the folded heading has to report.
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
  get v() { return params.get("v") || "a"; },
  get device() { return params.get("device") || "desktop"; },
  get scene() { return params.get("scene") || "rest"; },
  get days() { return Number(params.get("days") || 3); },
};

let ui = {
  pinned: ["moa", "winerim", "ourown"], // A: the owner's own order; B/C: "always show"
  quiet: [], // B/C: moved to Quiet by hand
  foldOpen: false,
  sectionOpen: true,
  event: null, // owner id an event just woke
  active: "moa",
  menu: null, // { id, x, y } on desktop, { id } on the phone
  drag: null, // { id, over: "top" | "fold" | ownerId }
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

// needsYou — what brings a folded owner back on its own. Its own question or
// error, something it wrote you have not read, a child stopped on you, or an
// event that just landed on it (a client's email). Working alone does not: a
// scheduled job is not a reason to take a row.
function needsYou(o, u) {
  const st = ownerState(o);
  if (st === "asks" || st === "unread") return true;
  if (childrenSummary(o.children).waiting > 0) return true;
  return u.event === o.id;
}

function isWorking(o) {
  return ownerState(o) === "working" || childrenSummary(o.children).working > 0;
}

function split(owners, u, variant, days) {
  const byId = new Map(owners.map((o) => [o.id, o]));
  const pinned = u.pinned.map((id) => byId.get(id)).filter(Boolean);
  const pinnedSet = new Set(u.pinned);
  const quietSet = new Set(u.quiet);
  const rest = owners.filter((o) => !pinnedSet.has(o.id));
  const recent = (o) => now - (o.lastUsed || 0) <= days * DAY;
  const up = (o) => needsYou(o, u) || (variant !== "a" && recent(o) && !quietSet.has(o.id));
  // The ones that rose sit under the pinned, most urgent first then most
  // recent: the pinned order is the owner's and nothing reshuffles it.
  const risen = rest.filter(up).sort((a, b) => (needsYou(b, u) - needsYou(a, u)) || (b.lastUsed - a.lastUsed));
  const folded = rest.filter((o) => !up(o)).sort((a, b) => b.lastUsed - a.lastUsed);
  return { top: [...pinned, ...risen], pinnedSet, folded };
}

// The folded owners said in words, as the owner row says them
// (decisions/lenguaje-de-estado.md): only what moves, nothing when all rest.
// Nothing that waits on you can be in here — it would have risen.
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

const pin = (id, before = null) => setUI((u) => {
  const list = u.pinned.filter((x) => x !== id);
  const at = before ? list.indexOf(before) : -1;
  if (at >= 0) list.splice(at, 0, id); else list.push(id);
  return { pinned: list, quiet: u.quiet.filter((x) => x !== id), menu: null, drag: null };
});
const unpin = (id) => setUI((u) => ({ pinned: u.pinned.filter((x) => x !== id), menu: null, drag: null }));
const quiet = (id) => setUI((u) => ({ pinned: u.pinned.filter((x) => x !== id), quiet: [...new Set([...u.quiet, id])], menu: null, drag: null }));
const unquiet = (id) => setUI((u) => ({ quiet: u.quiet.filter((x) => x !== id), menu: null }));
const openOwner = (o) => setUI({ active: o.id, menu: null });

function menuItems(o, variant, placement, u) {
  const pinnedNow = u.pinned.includes(o.id);
  const items = [{ icon: MessageSquare, label: "Open", run: () => openOwner(o) }];
  if (variant === "a") {
    if (pinnedNow) items.push({ icon: PinOff, label: "Move to More", run: () => unpin(o.id) });
    else items.push({ icon: Pin, label: "Pin to top", run: () => pin(o.id) });
  } else {
    if (pinnedNow) items.push({ icon: PinOff, label: "Unpin", run: () => unpin(o.id) });
    else items.push({ icon: Pin, label: "Always show", run: () => pin(o.id) });
    if (placement === "top") items.push({ icon: EyeOff, label: "Move to Quiet", run: () => quiet(o.id) });
    else if (u.quiet.includes(o.id)) items.push({ icon: Eye, label: "Show when active", run: () => unquiet(o.id) });
  }
  items.push({ icon: Pencil, label: "Edit owner…", run: () => setUI({ menu: null }) });
  return items;
}

// ── Gestures: right-click / long press / drag ────────────────────────────

function frameXY(e) {
  const frame = e.currentTarget.closest(".desktop-lab-frame");
  if (!frame) return { x: e.clientX, y: e.clientY };
  const r = frame.getBoundingClientRect();
  const k = r.width / frame.offsetWidth || 1;
  return { x: (e.clientX - r.left) / k, y: (e.clientY - r.top) / k };
}

function useGestures(o, phone, zone) {
  const timer = useRef(null);
  const fired = useRef(false);
  if (phone) {
    const clear = () => { clearTimeout(timer.current); timer.current = null; };
    return {
      onPointerDown: () => {
        fired.current = false;
        clear();
        timer.current = setTimeout(() => { fired.current = true; setUI({ menu: { id: o.id } }); }, 450);
      },
      onPointerUp: clear,
      onPointerCancel: clear,
      onPointerLeave: clear,
      onContextMenu: (e) => e.preventDefault(),
      onClickCapture: (e) => { if (fired.current) { e.preventDefault(); e.stopPropagation(); fired.current = false; } },
    };
  }
  return {
    onContextMenu: (e) => { e.preventDefault(); setUI({ menu: { id: o.id, ...frameXY(e) } }); },
    draggable: true,
    onDragStart: (e) => {
      e.dataTransfer.effectAllowed = "move";
      e.dataTransfer.setData("text/plain", o.id);
      e.dataTransfer.setDragImage(dragGhost(o), 12, 14);
      // A frame later, so the browser has taken its drag image before the
      // row dims and the drop targets appear.
      requestAnimationFrame(() => setUI({ drag: { id: o.id, over: null, from: zone } }));
    },
    onDragEnd: () => setUI({ drag: null }),
  };
}

// The drag image: the owner's name on a small pill, not a snapshot of the row
// (the row sits inside a scaled frame with animated faces).
function dragGhost(o) {
  let el = document.getElementById("ofl-ghost");
  if (!el) {
    el = document.createElement("div");
    el.id = "ofl-ghost";
    el.className = "ofl-ghost";
    document.body.appendChild(el);
  }
  el.textContent = o.name;
  return el;
}

function dropZone(zone, variant, beforeId = null) {
  return {
    onDragOver: (e) => {
      if (!ui.drag) return;
      e.preventDefault();
      e.stopPropagation();
      const over = beforeId || zone;
      if (ui.drag.over !== over) setUI((u) => ({ drag: { ...u.drag, over } }));
    },
    onDrop: (e) => {
      e.preventDefault();
      e.stopPropagation();
      const id = ui.drag?.id;
      if (!id) return;
      if (zone === "top") pin(id, beforeId && beforeId !== id ? beforeId : null);
      else if (variant === "a") unpin(id);
      else quiet(id);
    },
  };
}

// ── Rows ─────────────────────────────────────────────────────────────────

function TopRow({ o, phone, variant, pinned, u }) {
  const g = useGestures(o, phone, "top");
  const dragging = u.drag?.id === o.id;
  const over = u.drag && u.drag.over === o.id && u.drag.id !== o.id;
  return (
    <div
      class={`zl-session ofl-slot${dragging ? " is-dragging" : ""}${over ? " is-drop-before" : ""}`}
      data-owner={o.id}
      {...g}
      {...(!phone ? dropZone("top", variant, o.id) : null)}
    >
      <OwnerRow owner={o} active={o.id === u.active} onOpen={openOwner} />
      {!phone && (
        <button
          type="button"
          class={`ofl-pin${pinned ? " is-on" : ""}`}
          title={pinned ? (variant === "a" ? "Move to More" : "Unpin") : variant === "a" ? "Pin to top" : "Always show"}
          aria-label={pinned ? `Unpin ${o.name}` : `Pin ${o.name}`}
          onClick={(e) => { e.stopPropagation(); pinned ? unpin(o.id) : pin(o.id); }}
        >
          {pinned ? <PinOff size={14} /> : <Pin size={14} />}
        </button>
      )}
    </div>
  );
}

function FoldedRow({ o, phone, variant, u }) {
  const g = useGestures(o, phone, "fold");
  const working = isWorking(o);
  const st = ownerState(o);
  return (
    <div class={`ofl-cslot${u.drag?.id === o.id ? " is-dragging" : ""}`} {...g}>
      <button
        type="button"
        class={`zl-row ofl-crow${o.id === u.active ? " is-current" : ""}`}
        onClick={() => openOwner(o)}
        aria-label={`${o.name}, project owner${working ? ", working" : ""}`}
      >
        <OwnerAvatarFor owner={o} state={st} size={phone ? 24 : 20} />
        <span class="ofl-crow-name">{o.name}</span>
        {working
          ? <span class="ofl-crow-state">working</span>
          : <span class="ofl-crow-age zl-data">{age(o.lastUsed)}</span>}
      </button>
      {!phone && (
        <button
          type="button"
          class="ofl-pin"
          title={variant === "a" ? "Pin to top" : "Always show"}
          aria-label={`Pin ${o.name}`}
          onClick={(e) => { e.stopPropagation(); pin(o.id); }}
        >
          <Pin size={14} />
        </button>
      )}
    </div>
  );
}

// The fold's one row: whose faces are inside, how many, and what moves.
function FoldHead({ folded, label, open, onToggle, phone, variant, u }) {
  const summary = foldSummary(folded);
  const faces = folded.slice(0, 3);
  const over = u.drag && u.drag.over === "fold";
  return (
    <button
      type="button"
      class={`ofl-fold${open ? " is-open" : ""}${over ? " is-drop" : ""}${phone ? " is-phone" : ""}`}
      aria-expanded={open}
      aria-label={`${folded.length} ${label}${summary ? `, ${summary}` : ""}`}
      onClick={onToggle}
      {...(!phone ? dropZone("fold", variant) : null)}
    >
      <span class="ofl-stack" aria-hidden="true">
        {faces.map((o) => (
          <span class="ofl-stack-face" key={o.id}><OwnerAvatarFor owner={o} state="idle" size={phone ? 20 : 18} /></span>
        ))}
      </span>
      <span class="ofl-fold-label">
        <span class="ofl-fold-n">{folded.length}</span> {label}
      </span>
      {summary && <span class="ofl-fold-sum">{summary}</span>}
      <ChevronRight class="ofl-fold-chev" size={14} aria-hidden="true" />
    </button>
  );
}

function Shelf({ folded, phone, variant, u }) {
  const summary = foldSummary(folded);
  const over = u.drag && u.drag.over === "fold";
  return (
    <div class={`ofl-shelf${over ? " is-drop" : ""}${phone ? " is-phone" : ""}`} {...(!phone ? dropZone("fold", variant) : null)}>
      <div class="ofl-shelf-head">
        <span>Quiet</span>
        <span class="zl-data ofl-shelf-n">{folded.length}</span>
        {summary && <span class="ofl-fold-sum">{summary}</span>}
      </div>
      <div class="ofl-shelf-grid">
        {folded.map((o) => <ShelfFace key={o.id} o={o} phone={phone} u={u} />)}
      </div>
    </div>
  );
}

function ShelfFace({ o, phone, u }) {
  const g = useGestures(o, phone, "fold");
  const working = isWorking(o);
  return (
    <button
      type="button"
      class={`ofl-face${o.id === u.active ? " is-current" : ""}${working ? " is-working" : ""}${u.drag?.id === o.id ? " is-dragging" : ""}`}
      title={`${o.name} · ${working ? "working" : `last used ${age(o.lastUsed)} ago`}`}
      aria-label={`${o.name}, project owner${working ? ", working" : ""}`}
      onClick={() => openOwner(o)}
      {...g}
    >
      <OwnerAvatarFor owner={o} state={ownerState(o)} size={phone ? 36 : 32} />
      <span class="ofl-face-name">{o.name}</span>
      {working && <span class="ofl-face-state">working</span>}
    </button>
  );
}

// ── The block that replaces the OWNERS section ───────────────────────────

function OwnersBlock({ phone }) {
  const u = useUI();
  const owners = useOwners();
  const variant = lab.v;
  const { top, pinnedSet, folded } = split(owners, u, variant, lab.days);
  const label = variant === "a" ? "more" : "quiet";
  const topOver = u.drag && u.drag.over === "top";
  return (
    <div class={`ofl-block v-${variant}${phone ? " is-phone" : ""}${u.drag ? " is-dragging-any" : ""}`}>
      <SectionHead
        label="Owners"
        n={owners.length}
        open={u.sectionOpen}
        dot={null}
        onToggle={() => setUI({ sectionOpen: !u.sectionOpen })}
      />
      {u.sectionOpen && (
        <>
          <div class={`ofl-top${topOver ? " is-drop" : ""}`} {...(!phone ? dropZone("top", variant) : null)}>
            {top.map((o) => (
              <TopRow key={o.id} o={o} phone={phone} variant={variant} pinned={pinnedSet.has(o.id)} u={u} />
            ))}
            {u.drag && u.drag.from === "fold" && <div class="ofl-drop-hint">Drop to {variant === "a" ? "pin" : "always show"}</div>}
          </div>
          {folded.length > 0 && variant !== "c" && (
            <>
              <FoldHead
                folded={folded}
                label={label}
                open={u.foldOpen}
                onToggle={() => setUI({ foldOpen: !u.foldOpen })}
                phone={phone}
                variant={variant}
                u={u}
              />
              {u.foldOpen && (
                <div class="ofl-folded">
                  {folded.map((o) => <FoldedRow key={o.id} o={o} phone={phone} variant={variant} u={u} />)}
                </div>
              )}
            </>
          )}
          {folded.length > 0 && variant === "c" && <Shelf folded={folded} phone={phone} variant={variant} u={u} />}
          <button type="button" class="ow-newowner ofl-newowner">
            <span aria-hidden="true">+</span>
            New owner
          </button>
        </>
      )}
      {!phone && u.menu && <DesktopMenu owners={owners} variant={variant} />}
    </div>
  );
}

function DesktopMenu({ owners, variant }) {
  const u = useUI();
  const o = owners.find((x) => x.id === u.menu.id);
  const host = document.querySelector(".ofl-stage .desktop-lab-frame");
  useEffect(() => {
    const close = (e) => { if (!e.target.closest?.(".ofl-menu")) setUI({ menu: null }); };
    const esc = (e) => { if (e.key === "Escape") setUI({ menu: null }); };
    const t = setTimeout(() => document.addEventListener("pointerdown", close), 0);
    document.addEventListener("keydown", esc);
    return () => { clearTimeout(t); document.removeEventListener("pointerdown", close); document.removeEventListener("keydown", esc); };
  }, []);
  if (!o || !host) return null;
  const { top } = split(owners, u, variant, lab.days);
  const placement = top.some((x) => x.id === o.id) ? "top" : "fold";
  return createPortal(
    <div class="ofl-menu" role="menu" style={{ left: `${u.menu.x}px`, top: `${u.menu.y}px` }}>
      <div class="ofl-menu-head">
        <OwnerAvatarFor owner={o} state={ownerState(o)} size={20} />
        <span>{o.name}</span>
      </div>
      {menuItems(o, variant, placement, u).map((it) => (
        <button type="button" role="menuitem" class="ofl-menu-item" onClick={it.run} key={it.label}>
          <it.icon size={15} aria-hidden="true" />
          {it.label}
        </button>
      ))}
    </div>,
    host,
  );
}

function PhoneMenu() {
  const u = useUI();
  const owners = useOwners();
  const host = usePortalHost(".ofl-stage .mconv");
  const o = u.menu ? owners.find((x) => x.id === u.menu.id) : null;
  const variant = lab.v;
  const placement = o && split(owners, u, variant, lab.days).top.some((x) => x.id === o.id) ? "top" : "fold";
  if (!host) return null;
  return createPortal(
    <MobileSheet open={!!o} onClose={() => setUI({ menu: null })} title={o?.name || ""}>
      {o && (
        <div class="ofl-sheet">
          {menuItems(o, variant, placement, u).map((it) => (
            <button type="button" class="ofl-sheet-item" onClick={it.run} key={it.label}>
              <it.icon size={18} aria-hidden="true" />
              {it.label}
            </button>
          ))}
        </div>
      )}
    </MobileSheet>,
    host,
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
      <PhoneMenu />
    </>
  );
}

// ── Seed, scenes, lab bar ────────────────────────────────────────────────

function seed() {
  const scene = lab.scene;
  const phone = lab.device === "phone";
  const v = lab.v;
  ui = {
    ...ui,
    foldOpen: scene === "open" || scene === "menu" || scene === "drag",
    event: scene === "event" ? "albeniz" : null,
    menu: null,
    drag: null,
  };
  if (scene === "menu") {
    ui.menu = phone ? { id: v === "a" ? "pulse" : "ourown" } : { id: v === "a" ? "pulse" : "ourown", x: 170, y: v === "a" ? 560 : 330 };
  }
  if (scene === "drag") ui.drag = { id: "pulse", over: "top", from: "fold" };
  const ss = sessions();
  const owners = OWNERS.map((o) => (scene === "event" && o.id === "albeniz"
    ? { ...o, session_state: "running", lastUsed: LAST_USED.albeniz }
    : o));
  if (scene === "event") OWNER_REASON.albeniz = "Email from Laura Gil · reading";
  setState((s) => ({
    sessions: ss,
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

function useNoToasts() {
  useEffect(() => {
    const clear = () => getToasts().forEach((t) => removeToast(t.id));
    clear();
    return subscribeToasts(() => setTimeout(clear, 0));
  }, []);
}

const VARIANTS = [["now", "Today"], ["a", "A · Pinned + More"], ["b", "B · Quiet after N days"], ["c", "C · Face shelf"]];
const DEVICES = [["desktop", "Desktop"], ["phone", "Phone"]];
const SCENES = [["rest", "At rest"], ["open", "Fold open"], ["event", "Client email arrives"], ["menu", "Menu"], ["drag", "Dragging"]];
const DAYS = [["2", "2 days"], ["3", "3 days"], ["7", "7 days"]];

// Live: a client's email lands on a folded owner, or the owner is done with it.
function toggleEmail() {
  const on = ui.event !== "albeniz";
  if (on) OWNER_REASON.albeniz = "Email from Laura Gil · reading";
  else delete OWNER_REASON.albeniz;
  setState((s) => ({
    owners: { ...s.owners, list: s.owners.list.map((o) => (o.id === "albeniz" ? { ...o, session_state: on ? "running" : "saved" } : o)) },
  }));
  setUI({ event: on ? "albeniz" : null });
}

function EmailButton() {
  const u = useUI();
  return (
    <button type="button" class={`ofl-lab-opt ofl-lab-btn${u.event ? " is-on" : ""}`} onClick={toggleEmail}>
      {u.event ? "Albéniz: done with the email" : "Live: email to Clínica Albéniz"}
    </button>
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
      <Seg k="scene" cur={lab.scene} items={SCENES} />
      {(lab.v === "b" || lab.v === "c") && <Seg k="days" cur={String(lab.days)} items={DAYS} />}
      {lab.v !== "now" && <div class="ofl-lab-seg"><EmailButton /></div>}
    </div>
  );
}

export function OwnersFoldLab() {
  const [ready, setReady] = useState(false);
  useNoToasts();
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
