import { useStore } from "../../hooks/useStore.js";
import { OwnerChip } from "./Owners.jsx";
import { ownerOfSession } from "../../data/owners-model.js";
import { openOwnerConversation, ownersSlice } from "../../data/owners.js";

// OwnerChipEntry — the chip a CHILD session wears, and the door to its owner.
//
// It mounts only when the session's codebase has one: `ownerName` travels on
// the session itself (SessionInfo.owner_name), resolved where the session's
// book and its reporting were resolved, so the chip cannot claim an owner the
// session does not actually report to.
//
// Opening it needs the owner's `session_id`, which is on the owner and not on
// the child — hence the roster lookup. If the owners have not loaded yet the
// chip still shows the name it was given: what it says is true either way, and
// only the press has to wait.
export function OwnerChipEntry({ session, compact = false }) {
  const owners = useStore((s) => ownersSlice(s).list);
  const name = session?.ownerName || "";
  if (!name || (session?.kind || "") === "owner") return null;
  const owner = ownerOfSession(owners, session);
  return (
    <OwnerChip
      name={name}
      compact={compact}
      onClick={() => { if (owner) openOwnerConversation(owner); }}
    />
  );
}
