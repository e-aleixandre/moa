import { useEffect, useState } from "preact/hooks";
import { Plus, X } from "lucide-preact";
import { Field } from "../../primitives/Field/Field.jsx";
import { Button } from "../../primitives/Button/Button.jsx";
import { useStore } from "../../hooks/useStore.js";
import { ownersSlice } from "../../data/owners.js";
import { ownerEditBody, ownerState, sameSendTargets, sendCandidates, sendTargets } from "../../data/owners-model.js";
import { OwnerAvatarFor, ownerAvatar, storedAvatar } from "./OwnerAvatar.jsx";
import { OwnerCallLevel } from "./CallLevel.jsx";
import { OwnerIdentityPicker } from "./OwnerIdentityPicker.jsx";

/* EditOwner — changing an owner's name and face after it exists.

   It is a PAGE of the owner's dossier, pushed from Overview, not a dialog: the
   panel's own rule is that no modal ever opens over it (data/session-panel.js),
   and on a phone the panel IS a bottom sheet, so a second sheet put two
   grabbers, two headers and two ✕ over the same content.

   The same picker as New owner: choosing a face and changing it are the same
   act, and a second picker would be a second place for the palette to drift.

   WHAT IS NOT HERE: the eyes. They are the owner's state, and nobody chooses
   a state — the editor offers the three axes that are identity and no more.

   It opens on `ownerAvatar(owner)` rather than on `owner.avatar`, because an
   owner that never had a face chosen is drawn from the deterministic default
   and the editor must open on the face the user is looking at, not on an
   empty object. Saving then writes it, which is the moment a derived face
   becomes a stored one — from creation onwards the name and the face are
   independent, and renaming never moves the face.

   "Can send tasks to" is the owner's one-way permission to send tasks to
   other owners. It saves with the rest of the form: removing an owner there
   is a revocation, and one Save is the moment it is decided. */

// SendTo — the owners this one can send tasks to, × to take one out, and
// "+ Add" opening the others in place (no popover over the panel).
function SendTo({ owner, owners, ids, onChange, phone }) {
  const [adding, setAdding] = useState(false);
  const targets = sendTargets(owner, owners, ids);
  const candidates = sendCandidates(owner, owners, ids);
  const size = phone ? 24 : 20;
  return (
    <div class="ow-field">
      <span class="ow-label">Can send tasks to</span>
      <div class="ow-sendto" role="group" aria-label="Can send tasks to">
        {targets.map((o) => (
          <div class="ow-sendto-row" key={o.id}>
            <OwnerAvatarFor owner={o} state={ownerState(o)} size={size} />
            <span class="ow-entry-t">{o.name}</span>
            <button
              type="button"
              class="ow-sendto-x"
              aria-label={`Remove ${o.name}`}
              title={`Remove ${o.name}`}
              onClick={() => onChange(ids.filter((id) => id !== o.id))}
            >
              <X aria-hidden="true" />
            </button>
          </div>
        ))}
        {adding && candidates.map((o) => (
          <button
            type="button"
            class="ow-entry ow-sendto-pick"
            key={o.id}
            onClick={() => { onChange([...ids, o.id]); setAdding(false); }}
          >
            <OwnerAvatarFor owner={o} state={ownerState(o)} size={size} />
            <span class="ow-entry-t">{o.name}</span>
            <Plus aria-hidden="true" />
          </button>
        ))}
        {candidates.length > 0 && (
          <button type="button" class="ow-entry ow-sendto-add" aria-expanded={adding} onClick={() => setAdding(!adding)}>
            {adding ? <X aria-hidden="true" /> : <Plus aria-hidden="true" />}
            <span class="ow-entry-t">{adding ? "Cancel" : "Add"}</span>
          </button>
        )}
        {!targets.length && !candidates.length && <p class="ow-hint ow-sendto-none">There are no other owners yet.</p>}
      </div>
      {!targets.length && candidates.length > 0 && !adding && (
        <p class="ow-hint">Add the owners it can send tasks to without asking you.</p>
      )}
    </div>
  );
}

export function EditOwner({ owner, onSave, onClose, phone }) {
  const owners = useStore((st) => ownersSlice(st).list);
  const current = ownerAvatar(owner);
  const savedSendTo = owner.can_send_tasks_to || [];
  const [name, setName] = useState(owner.name);
  const [avatar, setAvatar] = useState(current);
  const [sendTo, setSendTo] = useState(savedSendTo);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState(null);
  useEffect(() => {
    setName(owner.name);
    setAvatar(ownerAvatar(owner));
    setSendTo(owner.can_send_tasks_to || []);
    setFailure(null);
  }, [owner.id, owner.name, owner.avatar?.shape, owner.avatar?.color, owner.avatar?.tone, savedSendTo.join(",")]);

  const cleanName = name.trim();
  const changed = cleanName !== owner.name
    || avatar.shape !== current.shape
    || avatar.color !== current.color
    || avatar.tone !== current.tone
    || !sameSendTargets(sendTo, savedSendTo);
  return (
    <div class="ow-form">
      <OwnerIdentityPicker
        name={name}
        shape={avatar.shape}
        color={avatar.color}
        tone={avatar.tone}
        seedKey={owner.codebase_key || owner.name}
        onShape={(shape) => setAvatar({ ...avatar, shape })}
        onColor={(color) => setAvatar({ ...avatar, color })}
        onTone={(tone) => setAvatar({ ...avatar, tone })}
      />
      <label class="ow-field">
        <span class="ow-label">Name</span>
        <Field
          variant="box"
          size="lg"
          value={name}
          onInput={(event) => setName(event.currentTarget.value)}
          aria-label="Owner name"
        />
      </label>
      <SendTo owner={owner} owners={owners} ids={sendTo} onChange={setSendTo} phone={phone} />
      <OwnerCallLevel owner={owner} />
      <div class="ow-form-foot">
        {failure && <p class="ow-fail" role="alert">{failure}</p>}
        <Button
          variant="accent"
          size="lg"
          className="ow-cta"
          disabled={busy || !cleanName || !changed}
          onClick={async () => {
            setBusy(true);
            setFailure(null);
            try {
              await onSave?.(ownerEditBody(owner, { name: cleanName, avatar: storedAvatar(avatar), sendTo }));
              onClose?.();
            } catch (error) {
              // Owners' errors are plain text after the status.
              setFailure(String(error.message || error).replace(/^\d{3}:\s*/, ""));
            } finally {
              setBusy(false);
            }
          }}
        >
          {busy ? "Saving…" : "Save changes"}
        </Button>
      </div>
      {phone && <div class="ow-form-pad" aria-hidden="true" />}
    </div>
  );
}
