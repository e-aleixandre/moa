import { useEffect, useMemo, useState } from "preact/hooks";
import { ArrowUp, ArrowUpRight, Check, ChevronRight, Info, KeyRound, Lock, MessageCircleQuestionMark, SquareTerminal, TriangleAlert } from "lucide-preact";
import { useStore } from "../../hooks/useStore.js";
import { useEdgeSwipeBack } from "../../hooks/useEdgeSwipeBack.js";
import { forYouSlice, forgetForYouItem, isWaitingItem, loadForYou, markNewsRead, orderWaiting } from "../../data/for-you.js";
import { forYouScopeIds } from "../../data/for-you-session.js";
import { ownersSlice } from "../../data/owners.js";
import { navigate } from "../../data/router.js";
import { resolvePermission } from "../../data/session-actions.js";
import { addToast } from "../../data/notifications.js";
import { openTaskSession, BackIcon } from "../Tasks/parts.jsx";
import { OwnerAvatarFor } from "../Owners/OwnerAvatar.jsx";
import { AskUserPrompt } from "../AskUserCard/AskUserPrompt.jsx";
import "./ForYou.css";
import { Requests } from "./Requests.jsx";
import { RecurringWatch } from "../Tasks/ScheduledVisible.jsx";
import { QuestionCall, StopQuestionCall } from "./QuestionCall.jsx";

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
function useRows(scope) {
  const all = useStore((s) => forYouSlice(s).items);
  const items = useMemo(() => (scope ? all.filter((it) => scope.has(it.session_id)) : all), [all, scope]);
  const loaded = useStore((s) => forYouSlice(s).loaded);
  const sessions = useStore((s) => s.sessions);
  const owners = useStore((s) => ownersSlice(s).list);
  const rows = useMemo(() => {
    const ownerBySession = new Map(owners.filter((o) => o.session_id).map((o) => [o.session_id, o]));
    const ownerById = new Map(owners.map((o) => [o.id, o]));
    const list = orderWaiting(items).map((item) => {
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
    return list;
  }, [items, sessions, owners]);
  return { rows, loaded };
}

function KindIcon({ item }) {
  if (item.kind === "permission") return <SquareTerminal size={15} aria-hidden="true" />;
  if (item.kind === "secret") return <KeyRound size={15} aria-hidden="true" />;
  if (item.requires_tap) return <Lock size={15} aria-hidden="true" />;
  return <MessageCircleQuestionMark size={15} aria-hidden="true" />;
}

// onLeave: the host (the header's sheet) closes itself when an action takes
// the user to a conversation or to Tasks — also when it is the one already
// on screen, where nothing else would change and the sheet would stay over it.
function openConversation(sessionId, onLeave) {
  onLeave?.();
  navigate(null);
  openTaskSession(sessionId);
}

function Answer({ row, phone, onDone, onLeave }) {
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
        <button type="button" class={`fy-btn is-primary${phone ? " is-wide" : ""}`} onClick={() => openConversation(item.session_id, onLeave)}>
          <KeyRound size={15} aria-hidden="true" />Enter password
        </button>
      </div>
    </div>
  );
}

export function Row({ row, open, phone, onToggle, onLeave }) {
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
          {!open && item.questions?.[0]?.context && <span class="fy-ctx">{item.questions[0].context}</span>}
          <span class="fy-meta">
            <span class="fy-mi fy-mi-who">
              {owner && <OwnerAvatarFor owner={owner} size={16} />}
              <span class="fy-who">{row.ownConversation ? row.session : owner ? `${owner.name} · ${row.session}` : row.session}</span>
            </span>
            <span class="fy-mi fy-age zl-data">{age(item.since)}</span>
            {more > 0 && <span class="fy-mi">+{more} question{more > 1 ? "s" : ""}</span>}
            {item.blocking
              ? <span class="fy-mi fy-block">{item.kind === "secret" ? "Stopped until you enter it" : "Stopped until you answer"}</span>
              : <span class="fy-mi fy-keeps">Keeps working</span>}
            {item.call_state && <span class="fy-mi"><QuestionCall item={item} /></span>}
            {item.requires_tap && phone && <span class="fy-mi fy-tap"><Lock size={12} aria-hidden="true" />Needs your tap</span>}
          </span>
        </span>
        {item.requires_tap && !phone && <span class="fy-chip"><Lock size={12} aria-hidden="true" />Needs your tap</span>}
        <ChevronRight size={16} class="fy-chev" aria-hidden="true" />
      </button>
      {open && (
        <div class={`fy-body${(item.questions?.length || 0) === 1 ? " is-single" : ""}`}>
          {item.requires_tap && phone && <span class="fy-chip"><Lock size={12} aria-hidden="true" />Needs your tap</span>}
          <Answer row={row} phone={phone} onDone={done} onLeave={onLeave} />
          <StopQuestionCall item={item} />
          <button type="button" class="fy-open" onClick={() => openConversation(item.session_id, onLeave)}>
            Open conversation<ArrowUpRight size={14} aria-hidden="true" />
          </button>
        </div>
      )}
    </li>
  );
}

function whoOf(sessionId, sessions, owners) {
  const own = owners.find((o) => o.session_id === sessionId);
  if (own) return { owner: own, who: own.name };
  const sess = sessions[sessionId];
  const owner = sess?.ownerId ? owners.find((o) => o.id === sess.ownerId) : null;
  const title = sess?.title || "A session";
  return { owner, who: owner ? `${owner.name} · ${title}` : title };
}

const NEWS_ICON = { done: Check, deployed: ArrowUp, problem: TriangleAlert, info: Info };

function NewsRow({ n, open, phone, onToggle, onLeave }) {
  const sessions = useStore((s) => s.sessions);
  const owners = useStore((s) => ownersSlice(s).list);
  const { owner, who } = whoOf(n.session_id, sessions, owners);
  const Icon = NEWS_ICON[n.kind] || Info;
  return (
    <li class={`fy-row fy-news is-${n.kind}${open ? " is-open" : ""}${n.read_at ? " is-read" : " is-unread"}`} data-news={n.id}>
      <button type="button" class="fy-row-head" aria-expanded={open} onClick={onToggle}>
        <span class={`fy-ico is-news-${n.kind}`}><Icon size={15} aria-hidden="true" /></span>
        <span class="fy-main">
          <span class="fy-title">{n.title}</span>
          {!open && n.body && <span class="fy-ctx">{n.body}</span>}
          <span class="fy-meta">
            <span class="fy-mi fy-mi-who">
              {owner && <OwnerAvatarFor owner={owner} size={16} />}
              <span class="fy-who">{who}</span>
            </span>
            <span class="fy-mi fy-age zl-data">{age(n.updated)}</span>
          </span>
        </span>
        {!n.read_at && <span class="fy-unread" aria-label="Unread" />}
        <ChevronRight size={16} class="fy-chev" aria-hidden="true" />
      </button>
      {open && (
        <div class="fy-body">
          {n.body && <p class="fy-news-body">{n.body}</p>}
          <button type="button" class="fy-open" onClick={() => openConversation(n.session_id, onLeave)}>
            Open conversation<ArrowUpRight size={14} aria-hidden="true" />
          </button>
        </div>
      )}
    </li>
  );
}

// News — what sessions chose to tell the user. Opening an item reads it,
// which archives it; it stays in place until the screen is left, so it does
// not jump away under the finger.
function News({ phone, scope, onLeave }) {
  const allNews = useStore((s) => forYouSlice(s).news);
  const news = useMemo(() => (scope ? allNews.filter((n) => scope.has(n.session_id)) : allNews), [allNews, scope]);
  const [openId, setOpenId] = useState(null);
  const [kept] = useState(() => new Set());
  // In one session's sheet the read News are the point of opening it with
  // nothing new, so they start unfolded when nothing else is shown.
  const [earlier, setEarlier] = useState(() => !!scope && !news.some((n) => !n.read_at));
  for (const n of news) if (!n.read_at) kept.add(n.id);
  const fresh = news.filter((n) => kept.has(n.id));
  const old = news.filter((n) => !kept.has(n.id));
  const unread = news.filter((n) => !n.read_at).length;
  if (news.length === 0) return null;
  const toggle = (n) => {
    if (!n.read_at) markNewsRead([n]);
    setOpenId(openId === n.id ? null : n.id);
  };
  // Only read News: no heading over nothing, just the fold to find them.
  if (fresh.length === 0) {
    return (
      <section class="fy-sec fy-news-old">
        <button type="button" class="fy-earlier" aria-expanded={earlier} onClick={() => setEarlier(!earlier)}>
          Earlier news · {old.length}<ChevronRight size={14} class={earlier ? "is-open" : ""} aria-hidden="true" />
        </button>
        {earlier && (
          <ul class="fy-list">
            {old.map((n) => <NewsRow key={n.id} n={n} phone={phone} onLeave={onLeave} open={openId === n.id} onToggle={() => toggle(n)} />)}
          </ul>
        )}
      </section>
    );
  }
  return (
    <section class="fy-sec">
      <div class="fy-sec-head">
        <span class="fy-sec-t">News{unread > 0 && <span class="fy-sec-new"> · {unread} new</span>}</span>
        {unread > 0 && <button type="button" class="fy-sec-act" onClick={() => markNewsRead(news.filter((n) => !n.read_at))}>Mark all read</button>}
      </div>
      <ul class="fy-list">
        {fresh.map((n) => <NewsRow key={n.id} n={n} phone={phone} onLeave={onLeave} open={openId === n.id} onToggle={() => toggle(n)} />)}
      </ul>
      {old.length > 0 && (
        <>
          <button type="button" class="fy-earlier" aria-expanded={earlier} onClick={() => setEarlier(!earlier)}>
            Earlier · {old.length}<ChevronRight size={14} class={earlier ? "is-open" : ""} aria-hidden="true" />
          </button>
          {earlier && (
            <ul class="fy-list">
              {old.map((n) => <NewsRow key={n.id} n={n} phone={phone} onLeave={onLeave} open={openId === n.id} onToggle={() => toggle(n)} />)}
            </ul>
          )}
        </>
      )}
    </section>
  );
}

// List — the whole For you, or with `scope` (a Set of session ids) one
// conversation's share of it: the same rows and News, filtered, never a copy.
// What asks or stops a session first, then News, then the agents' requests
// folded under one line. Nothing opens by itself: it is read at a glance.
// `onReturn` reopens the host (the header's sheet) when the user comes back
// from Tasks.
function List({ phone, scope, onOpenAll, onLeave, onReturn }) {
  const { rows, loaded } = useRows(scope);
  const allNews = useStore((s) => forYouSlice(s).news);
  const news = useMemo(() => (scope ? allNews.filter((n) => scope.has(n.session_id)) : allNews), [allNews, scope]);
  const [openId, setOpenId] = useState(null);
  useEffect(() => { loadForYou(); }, []);
  const waiting = rows.filter((r) => isWaitingItem(r.item));
  const requests = rows.filter((r) => !isWaitingItem(r.item));
  const blocking = waiting.filter((r) => r.item.blocking).length;
  const upToDate = loaded && waiting.length === 0 && requests.length === 0 && !news.some((n) => !n.read_at);
  return (
    <>
      {waiting.length > 0 && (
        <section class="fy-sec">
          <div class="fy-sec-head">
            <span class="fy-sec-t">Waiting on you</span>
            <span class="fy-sum"><span class="fy-sum-n">{waiting.length} waiting</span>{blocking > 0 && <span class="fy-sum-b"> · {blocking} stopped</span>}</span>
          </div>
          <ul class="fy-list">
            {waiting.map((r) => (
              <Row
                key={r.item.id}
                row={r}
                phone={phone}
                onLeave={onLeave}
                open={openId === r.item.id}
                onToggle={() => setOpenId(openId === r.item.id ? null : r.item.id)}
              />
            ))}
          </ul>
        </section>
      )}
      {!scope && <RecurringWatch phone={phone} />}
      {upToDate && <div class="fy-empty">{scope ? "Nothing is waiting on you here." : "You are up to date."}</div>}
      {scope && upToDate && news.length === 0 && onOpenAll && (
        <button type="button" class="fy-open fy-all" onClick={onOpenAll}>See everything in For you<ArrowUpRight size={14} aria-hidden="true" /></button>
      )}
      <News phone={phone} scope={scope} onLeave={onLeave} />
      <Requests rows={requests} phone={phone} openId={openId} setOpenId={setOpenId} onLeave={onLeave} onReturn={onReturn} />
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

// SessionForYouList — the body of the header's sheet: what ONE conversation
// (and, for an owner, its sessions) waits on the user for, plus its News.
export function SessionForYouList({ sessionId, phone, onOpenAll, onLeave, onReturn }) {
  const sessions = useStore((s) => s.sessions);
  const owners = useStore((s) => ownersSlice(s).list);
  const scope = useMemo(() => forYouScopeIds(sessionId, sessions, owners), [sessionId, sessions, owners]);
  return <List phone={phone} scope={scope} onOpenAll={onOpenAll} onLeave={onLeave} onReturn={onReturn} />;
}
