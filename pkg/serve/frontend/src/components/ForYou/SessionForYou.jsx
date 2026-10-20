import { Inbox } from "lucide-preact";
import { useStore } from "../../hooks/useStore.js";
import { forYouForSession, sessionForYouLabel } from "../../data/for-you-session.js";
import { ownersSlice } from "../../data/owners.js";
import { openForYouView } from "../../data/for-you.js";
import { Sheet } from "../Sheet/Sheet.jsx";
import { MobileSheet } from "../../layout/mobile/MobileSheet/MobileSheet.jsx";
import { SessionForYouList } from "./ForYou.jsx";
import "./ForYou.css";

// useSessionForYou — the header button's two numbers for one conversation:
// what it waits on you for (an owner's count includes its sessions') and its
// unread News. Primitives, so the header does not rebuild on every poll.
export function useSessionForYou(sessionId) {
  const waiting = useStore((s) => (sessionId ? forYouForSession(s, sessionId, ownersSlice(s).list).waiting : 0));
  const unread = useStore((s) => (sessionId ? forYouForSession(s, sessionId, ownersSlice(s).list).unread : 0));
  return { waiting, unread };
}

// SessionForYouButton — For you OF THIS CONVERSATION, top right of its head.
// Amber with the count when something waits, a violet dot for unread News,
// quiet when there is nothing — and always tappable, because the read News
// are only found again through it. `phone` is the capsule of the mobile
// chrome (MobileChrome.css); otherwise it is a head action of the desktop's.
export function SessionForYouButton({ waiting = 0, unread = 0, phone = false, open = false, onClick }) {
  const label = sessionForYouLabel(waiting, unread);
  return (
    <button
      type="button"
      class={phone ? "zl-cap zl-cap-right zl-cap-foryou" : `zl-desk-act fy-hd${waiting > 0 ? " has-items" : ""}${open ? " is-on" : ""}`}
      data-foryou-session="true"
      onClick={onClick}
      aria-label={label}
      aria-haspopup="dialog"
      aria-expanded={open}
      title={phone ? undefined : "For you in this conversation"}
    >
      <Inbox size={phone ? 18 : 16} aria-hidden="true" />
      {waiting > 0 && <span class="zl-cap-foryou-n zl-data">{waiting > 99 ? "99+" : waiting}</span>}
      {unread > 0 && <span class="zl-cap-foryou-dot" aria-hidden="true" />}
    </button>
  );
}

// SessionForYouSheet — what the button opens: the conversation's share of
// For you, the same rows as the general one, filtered. A bottom sheet on the
// phone, the centred sheet on the desktop.
// onReopen opens it again: Back from Tasks, opened from one of its rows,
// returns here.
export function SessionForYouSheet({ open, onClose, onReopen, sessionId, phone = false, scope = "This conversation" }) {
  const openAll = () => {
    onClose?.();
    openForYouView();
  };
  if (phone) {
    return (
      <MobileSheet open={open} onClose={onClose} title="For you" scope={scope}>
        {open && <div class="fy-phone fy-sheet"><SessionForYouList sessionId={sessionId} phone onOpenAll={openAll} onLeave={onClose} onReturn={onReopen} /></div>}
      </MobileSheet>
    );
  }
  return (
    <Sheet open={open} onClose={onClose} title="For you" class="fy-dsheet">
      {open && <SessionForYouList sessionId={sessionId} onOpenAll={openAll} onLeave={onClose} onReturn={onReopen} />}
    </Sheet>
  );
}
