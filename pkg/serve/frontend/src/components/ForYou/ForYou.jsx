import { useEffect, useMemo, useState } from "preact/hooks";
import { ArrowUpRight, ChevronRight, KeyRound, Lock, MessageCircleQuestionMark, SquareTerminal } from "lucide-preact";
import { useStore } from "../../hooks/useStore.js";
import { useEdgeSwipeBack } from "../../hooks/useEdgeSwipeBack.js";
import { forYouSlice, forgetForYouItem, loadForYou } from "../../data/for-you.js";
import { ownersSlice } from "../../data/owners.js";
import { navigate } from "../../data/router.js";
import { resolvePermission } from "../../data/session-actions.js";
import { addToast } from "../../data/notifications.js";
import { openTaskSession, BackIcon } from "../Tasks/parts.jsx";
import { OwnerAvatarFor } from "../Owners/OwnerAvatar.jsx";
import { AskUserPrompt } from "../AskUserCard/AskUserPrompt.jsx";
import "./ForYou.css";

function age(since) {
  const t = Date.parse(since || "");
  if (!t) return "";
  const min = Math.floor(Math.max(0, Date.now() - t) / 60000);
  if (min < 1) return "now";
  if (min < 60) return `${min}m`;
  const h = Math.floor(min / 60);
  return h < 24 ? `${h}h` : `${Math.floor(h / 24)}d`;
}

// permissionLine — what the tool wants to do, in its own words: the command
// for a shell call, the path for a file tool, else the tool's name.
function permissionLine(item) {
  const a = item.args || {};
  return a.command || a.path || a.file_path || a.url || item.tool || "";
}

// title — the row is the question, not the session: what you would answer.
function title(item) {
  if (item.kind === "permission") return item.tool === "bash" ? "Run this command?" : `Allow ${item.tool || "this tool"}?`;
  if (item.kind === "secret") return item.label ? `Enter the ${item.label}` : "Enter a password";
  return item.questions?.[0]?.question || "Question";
}

// joinItems puts each item next to who asked it: the session's title and the
// project owner it reports to, from the roster and the owners list.
function useRows() {
  const items = useStore((s) => forYouSlice(s).items);
  const loaded = useStore((s) => forYouSlice(s).loaded);
  const sessions = useStore((s) => s.sessions);
  const owners = useStore((s) => ownersSlice(s).list);
  const rows = useMemo(() => {
    const ownerBySession = new Map(owners.filter((o) => o.session_id).map((o) => [o.session_id, o]));
    const ownerById = new Map(owners.map((o) => [o.id, o]));
    const list = items.map((item) => {
      const sess = sessions[item.session_id];
      const ownConv = ownerBySession.get(item.session_id) || null;
      const owner = ownConv || (sess?.ownerId ? ownerById.get(sess.ownerId) : null) || null;
      return {
        item,
        owner,
        ownConversation: !!ownConv,
        session: ownConv ? ownConv.name : (sess?.title || "A session"),
      };
    });
    // What has stopped a session first, oldest first; then what does not
    // stop anything, oldest first.
    return [...list.filter((r) => r.item.blocking), ...list.filter((r) => !r.item.blocking)];
  }, [items, sessions, owners]);
  return { rows, loaded };
}

function KindIcon({ item }) {
  if (item.kind === "permission") return <SquareTerminal size={15} aria-hidden="true" />;
  if (item.kind === "secret") return <KeyRound size={15} aria-hidden="true" />;
  if (item.requires_tap) return <Lock size={15} aria-hidden="true" />;
  return <MessageCircleQuestionMark size={15} aria-hidden="true" />;
}

function openConversation(sessionId) {
  navigate(null);
  openTaskSession(sessionId);
}

function Answer({ row, phone, onDone }) {
  const { item } = row;
  const [busy, setBusy] = useState(false);
  if (item.kind === "question") {
    const ask = { id: item.id, questions: item.questions || [], async: !!item.async, requires_tap: !!item.requires_tap };
    return (
      <AskUserPrompt
        key={item.id}
        ask={ask}
        sessionId={item.session_id}
        compact
        onDone={onDone}
      />
    );
  }
  if (item.kind === "permission") {
    const decide = (approved) => {
      setBusy(true);
      resolvePermission(item.session_id, item.id, approved)
        .then(() => onDone(approved ? "allowed" : "denied"))
        .catch(() => {
          setBusy(false);
          addToast({ title: "Could not answer the permission", detail: "Open the conversation and answer it there.", type: "error" });
        });
    };
    return (
      <div class="fy-perm">
        <pre class="fy-cmd zl-data">{permissionLine(item)}</pre>
        <div class="fy-actions">
          <button type="button" class="fy-btn" disabled={busy} onClick={() => decide(false)}>Deny</button>
          <button type="button" class="fy-btn is-primary" disabled={busy} onClick={() => decide(true)}>Allow once</button>
        </div>
      </div>
    );
  }
  return (
    <div class="fy-secret">
      {item.command && <pre class="fy-cmd zl-data">{item.command}</pre>}
      <div class="fy-actions">
        <button type="button" class={`fy-btn is-primary${phone ? " is-wide" : ""}`} onClick={() => openConversation(item.session_id)}>
          <KeyRound size={15} aria-hidden="true" />Enter password
        </button>
      </div>
    </div>
  );
}

function Row({ row, open, phone, onToggle }) {
  const { item, owner } = row;
  const more = (item.questions?.length || 0) - 1;
  const done = (outcome) => {
    forgetForYouItem(item.id);
    if (outcome === "dismissed") addToast({ title: "Dismissed", detail: row.session, type: "info" });
  };
  return (
    <li class={`fy-row${open ? " is-open" : ""}${item.blocking ? " is-blocking" : ""}`} data-item={item.id}>
      <button type="button" class="fy-row-head" aria-expanded={open} onClick={onToggle}>
        <span class={`fy-ico is-${item.kind}`}><KindIcon item={item} /></span>
        <span class="fy-main">
          <span class="fy-title">{title(item)}</span>
          <span class="fy-meta">
            {owner && <OwnerAvatarFor owner={owner} size={16} />}
            <span class="fy-who">{row.ownConversation ? row.session : owner ? `${owner.name} · ${row.session}` : row.session}</span>
            <span class="fy-sep">·</span>
            <span class="fy-age zl-data">{age(item.since)}</span>
            {more > 0 && <><span class="fy-sep">·</span><span>+{more} question{more > 1 ? "s" : ""}</span></>}
            <span class="fy-sep">·</span>
            {item.blocking
              ? <span class="fy-block">{item.kind === "secret" ? "Stopped until you enter it" : "Stopped until you answer"}</span>
              : <span class="fy-keeps">Keeps working</span>}
          </span>
        </span>
        {item.requires_tap && !phone && <span class="fy-chip"><Lock size={12} aria-hidden="true" />Needs your tap</span>}
        <ChevronRight size={16} class="fy-chev" aria-hidden="true" />
      </button>
      {open && (
        <div class={`fy-body${(item.questions?.length || 0) === 1 ? " is-single" : ""}`}>
          {item.requires_tap && phone && <span class="fy-chip"><Lock size={12} aria-hidden="true" />Needs your tap</span>}
          <Answer row={row} phone={phone} onDone={done} />
          <button type="button" class="fy-open" onClick={() => openConversation(item.session_id)}>
            Open conversation<ArrowUpRight size={14} aria-hidden="true" />
          </button>
        </div>
      )}
    </li>
  );
}

function List({ phone }) {
  const { rows, loaded } = useRows();
  const [openId, setOpenId] = useState(null);
  useEffect(() => { loadForYou(); }, []);
  // The first row opens by itself: the screen exists to answer it.
  useEffect(() => {
    if (openId && rows.some((r) => r.item.id === openId)) return;
    setOpenId(rows[0]?.item.id || null);
  }, [rows, openId]);
  const blocking = rows.filter((r) => r.item.blocking).length;
  if (loaded && rows.length === 0) {
    return <div class="fy-empty">Nothing is waiting on you.</div>;
  }
  return (
    <>
      <div class="fy-sum">
        <span class="fy-sum-n">{rows.length} waiting</span>
        {blocking > 0 && <span class="fy-sum-b"> · {blocking} stopped</span>}
      </div>
      <ul class="fy-list">
        {rows.map((r) => (
          <Row
            key={r.item.id}
            row={r}
            phone={phone}
            open={openId === r.item.id}
            onToggle={() => setOpenId(openId === r.item.id ? "" : r.item.id)}
          />
        ))}
      </ul>
    </>
  );
}

// ForYouScreen — the desktop's middle zone.
export function ForYouScreen() {
  return (
    <main class="conversation-main fy-main" aria-label="For you">
      <header class="zl-desk-head fy-head">
        <span class="zl-crumb-title">For you</span>
      </header>
      <div class="fy-scroll"><div class="fy-col"><List /></div></div>
    </main>
  );
}

// MobileForYouView — the phone's full-screen push, with ‹ back.
export function MobileForYouView({ onBack }) {
  const { screenRef, dragging, swipeBind } = useEdgeSwipeBack({ onBack });
  return (
    <div class={dragging ? "minbox is-swiping" : "minbox"} ref={screenRef} {...swipeBind}>
      <div class="zi-inbox is-phone fy-phone">
        <div class="zi-head">
          <button type="button" class="zi-back" onClick={onBack} aria-label="Back to the conversation"><BackIcon /></button>
          <span class="zi-title">For you</span>
        </div>
        <div class="fy-scroll"><List phone /></div>
      </div>
    </div>
  );
}
