import { Archive, ArchiveRestore, Copy, MoreHorizontal, Trash2 } from "lucide-preact";
import { useEffect, useState } from "preact/hooks";
import { ActionMenu } from "../ActionMenu/ActionMenu.jsx";
import { copyToClipboard } from "../../data/util/format.js";
import "./SessionCardMenu.css";

// SessionCardMenu keeps session lifecycle actions with the session card while
// leaving card selection to the caller. It owns the popup lifecycle so mobile
// and desktop retain identical click-outside, Escape, and keyboard behavior.
export function SessionCardMenu({
  session,
  onClose,
  onReopen,
  onDelete,
  scrollContainerSelector,
  // An owner row uses this same menu and gesture. Its "session" is the owner
  // (`saved` = closed), the wording says so, and Delete is not offered: it
  // would leave the owner's sessions without an owner.
  owner = false,
}) {
  const [open, setOpen] = useState(false);
  const [confirmingDelete, setConfirmingDelete] = useState(false);

  useEffect(() => {
    if (!open) setConfirmingDelete(false);
  }, [open]);

  const stop = (event) => event.stopPropagation();

  const noun = owner ? "owner" : "session";
  const actions = [
    session.saved
      ? { id: "reopen", icon: ArchiveRestore, label: `Reopen ${noun}`, onClick: () => onReopen?.(session.id) }
      : { id: "close", icon: Archive, label: `Close ${noun}`, onClick: () => onClose?.(session.id) },
    { id: "copy", icon: Copy, label: `Copy ${noun} ID`, onClick: () => copyToClipboard(session.id) },
    ...(owner ? [] : [confirmingDelete
      ? { id: "delete", icon: Trash2, label: "Delete — this cannot be undone", danger: true, onClick: () => onDelete?.(session.id) }
      : { id: "delete", icon: Trash2, label: "Delete…", danger: true, closeOnClick: false, onClick: () => setConfirmingDelete(true) }]),
  ];

  return (
    <div class="session-card-menu" onClick={stop}>
      <ActionMenu
        actions={actions}
        open={open}
        onOpenChange={setOpen}
        icon={MoreHorizontal}
        label={owner ? "Owner actions" : "Session actions"}
        triggerClass="session-card-menu-button"
        triggerSize={16}
        placement="auto"
        align="end"
        scrollContainerSelector={scrollContainerSelector}
      />
    </div>
  );
}
