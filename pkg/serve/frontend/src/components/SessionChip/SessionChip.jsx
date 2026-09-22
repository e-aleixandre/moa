// SessionChip — a session named in the ledger, as the door to it.
//
// Two blocks name a session in an owner's conversation: what the owner SENT
// to it (SessionMessage) and what it REPORTED back (EventBlock). They used to
// say it two ways — a chip you tap in one direction, a row with a separate
// "Open" button in the other — for the same act of going there. One component
// makes them read the same whichever way the message travelled.
//
// The store is the only place a session id becomes a name and a live state. A
// session this client has not loaded keeps the caller's fallback name and
// state, and its chip does not offer to open what cannot be opened.
import { useStore } from "../../hooks/useStore.js";
import { StateDot } from "../../primitives/StateDot/StateDot.jsx";
import { sessionDotState } from "../../data/util/format.js";
import "./SessionChip.css";

// shortId is the fallback name for a session this client has never loaded (an
// old transcript, another project's roster). A 32-char id fills the line and
// says nothing more than its head does.
export function shortId(id) {
  const value = String(id || "");
  return value.length > 8 ? value.slice(0, 8) : value;
}

export function SessionChip({ sessionId, title = "", fallbackState = "saved", onOpen }) {
  const session = useStore((state) => (sessionId ? state.sessions[sessionId] : null));
  const name = (session?.title || "").trim() || String(title || "").trim() || shortId(sessionId);
  const openable = !!session && !!onOpen;
  return (
    <button
      type="button"
      class="session-chip"
      disabled={!openable}
      onClick={() => openable && onOpen(sessionId)}
      title={openable ? `Open ${name}` : name}
    >
      <StateDot state={session ? sessionDotState(session) : fallbackState} size={7} />
      <span class="session-chip-name">{name}</span>
    </button>
  );
}
