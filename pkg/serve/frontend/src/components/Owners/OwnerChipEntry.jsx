import { useStore } from "../../hooks/useStore.js";
import { OwnerAvatarFor } from "./OwnerAvatar.jsx";
import { ownerOfSession, ownerState } from "../../data/owners-model.js";
import { openOwnerConversation, ownersSlice } from "../../data/owners.js";

// useOwnerStatusItem — what a CHILD session's status strip says about its
// owner: a door to the owner's conversation, nothing more. It answers null
// when the session's codebase has no owner or the session IS the owner.
//
// `ownerName` travels on the session itself (SessionInfo.owner_name), resolved
// where the session's book and its reporting were resolved, so the strip cannot
// claim an owner the session does not actually report to. Opening it needs the
// owner's `session_id`, which is on the owner and not on the child — hence the
// roster lookup; until the owners have loaded the name is still true and only
// the press has to wait.
export function useOwnerStatusItem(session) {
  const owners = useStore((s) => ownersSlice(s).list);
  const name = session?.ownerName || "";
  if (!name || (session?.kind || "") === "owner") return null;
  const owner = ownerOfSession(owners, session);
  return {
    name,
    avatar: owner ? <OwnerAvatarFor owner={owner} state={ownerState(owner)} size={14} /> : null,
    onOpen: () => { if (owner) openOwnerConversation(owner); },
  };
}
