// SessionMention — a session id the assistant wrote in its prose, as the door
// to that session.
//
// The owner names sessions by id in inline code. markdown.js tags those spans
// (`code.session-ref`) from the text alone; here, at mount time, each tag
// becomes a SessionChip if this client knows the session, and stays the same
// inline code if it does not. The lookup is the store's id-keyed map, and each
// mention subscribes to its own id, so a session that loads later turns its
// mention into a chip without re-rendering the transcript.
import { render } from "preact";
import { useStore } from "../../hooks/useStore.js";
import { SESSION_REF_CLASS } from "../../data/util/markdown.js";
import { SessionChip } from "./SessionChip.jsx";
import "./SessionChip.css";

export function SessionMention({ sessionId, onOpen }) {
  const known = useStore((state) => !!state.sessions[sessionId]);
  if (!known) return <code>{sessionId}</code>;
  return <SessionChip sessionId={sessionId} onOpen={onOpen} />;
}

// mountSessionMentions swaps every tagged id under `root` for a SessionMention
// root and returns the cleanup that unmounts them. The html that `root` got
// through innerHTML is replaced wholesale on change, so the hosts are only
// ever detached, never re-diffed.
export function mountSessionMentions(root, onOpen) {
  if (!root) return () => {};
  const hosts = [];
  for (const code of root.querySelectorAll(`code.${SESSION_REF_CLASS}`)) {
    if (code.closest("pre")) continue;
    const host = document.createElement("span");
    host.className = "session-mention";
    code.replaceWith(host);
    render(<SessionMention sessionId={code.textContent} onOpen={onOpen} />, host);
    hosts.push(host);
  }
  return () => {
    for (const host of hosts) render(null, host);
  };
}
