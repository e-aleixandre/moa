import { MoreHorizontal } from "lucide-preact";
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
}) {
  const [open, setOpen] = useState(false);
  const [confirmingDelete, setConfirmingDelete] = useState(false);

  useEffect(() => {
    if (!open) setConfirmingDelete(false);
  }, [open]);

  const stop = (event) => event.stopPropagation();

  const actions = [
    session.saved
      ? { id: "reopen", label: "Reopen session", onClick: () => onReopen?.(session.id) }
      : { id: "close", label: "Close session", onClick: () => onClose?.(session.id) },
    { id: "copy", label: "Copy session ID", onClick: () => copyToClipboard(session.id) },
    confirmingDelete
      ? { id: "delete", label: "Delete — this cannot be undone", danger: true, onClick: () => onDelete?.(session.id) }
      : { id: "delete", label: "Delete…", danger: true, closeOnClick: false, onClick: () => setConfirmingDelete(true) },
  ];

  return (
    <div class="session-card-menu" onClick={stop}>
      <ActionMenu
        actions={actions}
        open={open}
        onOpenChange={setOpen}
        icon={MoreHorizontal}
        label="Session actions"
        triggerClass="session-card-menu-button"
        triggerSize={16}
        placement="auto"
        align="end"
        scrollContainerSelector={scrollContainerSelector}
      />
    </div>
  );
}
