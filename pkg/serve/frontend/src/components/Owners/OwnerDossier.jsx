import { useEffect, useState } from "preact/hooks";
import { useStore } from "../../hooks/useStore.js";
import { OwnerPanel } from "./Owners.jsx";
import { ownerRows } from "../../data/owners-model.js";
import {
  loadOwnerBook, openBookFile, ownersSlice, saveBookFile,
} from "../../data/owners.js";
import { closeSessionPanel } from "../../data/session-panel.js";
import { openSession } from "../../data/tile-actions.js";
import { addToast } from "../../data/notifications.js";

// OwnerDossier — the container that hands OwnerPanel the real owner, its real
// children and its real book. It mounts in the same zone a session's dossier
// occupies (DesktopDossier docked, MobileSheet on the phone), which is what
// makes an owner read as a peer of a session rather than a second application.
//
// The children are NOT fetched: they are selected out of the roster the store
// already holds (every session carries `ownerId`), so the dossier and the
// sidebar cannot disagree about which session is waiting. The book IS fetched
// — it is files on disk, and nothing else in the app knows them.
export function OwnerDossier({ session, open = true, variant = "", onClose }) {
  const slice = useStore(ownersSlice);
  const sessions = useStore((s) => s.sessions);
  const [tab, setTab] = useState("overview");

  const owner = ownerRows(slice.list, sessions).find((o) => o.session_id === session?.id) || null;
  const ownerId = owner?.id || null;

  // The book is read when the Book tab is first entered, not when the owner's
  // conversation is opened: reading a transcript must not cost a directory
  // walk nobody asked for.
  useEffect(() => {
    if (tab !== "book" || !ownerId) return;
    if (slice.bookOwnerId === ownerId && slice.bookStatus !== "idle") return;
    loadOwnerBook(ownerId);
  }, [tab, ownerId]);

  if (!owner) return null;

  const mine = slice.bookOwnerId === ownerId;
  const files = mine ? slice.bookFiles : [];
  const openPath = mine ? slice.openPath : null;
  // The panel reads {path,label,bytes,body}; the store holds the listing and
  // the one open file separately, so they are joined here rather than kept
  // joined in the store, where a stale body could outlive its listing.
  const book = files.map((f) => ({
    path: f.path,
    label: f.path.split("/").pop(),
    bytes: f.bytes,
    body: f.path === openPath ? slice.openBody : "",
    editable: f.path === openPath ? slice.openEditable : false,
  }));

  return (
    <OwnerPanel
      owner={owner}
      book={book}
      bookStatus={mine ? slice.bookStatus : "idle"}
      bookError={mine ? slice.bookError : null}
      fileStatus={mine ? slice.openStatus : "idle"}
      tab={tab}
      onTab={setTab}
      openPath={openPath}
      onOpenFile={(path) => openBookFile(ownerId, path)}
      onSave={(path, content) => {
        saveBookFile(ownerId, path, content).catch((error) => {
          addToast({
            title: "Could not save the index",
            detail: String(error.message || error),
            type: "error",
          });
        });
      }}
      onOpenChild={(child) => openSession(child.id)}
      onClose={onClose || closeSessionPanel}
      open={open}
      variant={variant}
    />
  );
}
