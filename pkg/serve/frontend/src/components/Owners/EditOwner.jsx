import { useEffect, useState } from "preact/hooks";
import { Field } from "../../primitives/Field/Field.jsx";
import { Button } from "../../primitives/Button/Button.jsx";
import { ownerAvatar, storedAvatar } from "./OwnerAvatar.jsx";
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
   independent, and renaming never moves the face. */
export function EditOwner({ owner, onSave, onClose, phone }) {
  const current = ownerAvatar(owner);
  const [name, setName] = useState(owner.name);
  const [avatar, setAvatar] = useState(current);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState(null);
  useEffect(() => {
    setName(owner.name);
    setAvatar(ownerAvatar(owner));
    setFailure(null);
  }, [owner.id, owner.name, owner.avatar?.shape, owner.avatar?.color, owner.avatar?.tone]);

  const cleanName = name.trim();
  const changed = cleanName !== owner.name
    || avatar.shape !== current.shape
    || avatar.color !== current.color
    || avatar.tone !== current.tone;
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
              await onSave?.({ name: cleanName, avatar: storedAvatar(avatar) });
              onClose?.();
            } catch (error) {
              setFailure(String(error.message || error));
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
