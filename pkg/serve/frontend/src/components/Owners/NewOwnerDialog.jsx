import { Sheet } from "../Sheet/Sheet.jsx";
import { MobileSheet } from "../../layout/mobile/MobileSheet/MobileSheet.jsx";
import { useState } from "preact/hooks";
import { NewOwner, modelPageBackLabel, modelPageParent, modelPageTitle } from "./Owners.jsx";

// NewOwnerDialog — where creating an owner happens now, in BOTH densities.
//
// It used to be a page pushed INSIDE the sidebar, which the owner rejected
// after using it on a phone: the column is where you look for your work, and a
// six-field form that takes it over hides the list it was launched from.
//
// The two encarnations are the product's own (CRITERIO §4 — one decision,
// adapted per density, not two inventions): the centred modal `Sheet` on the
// desktop, the bottom sheet `MobileSheet` on a phone — the same pair the
// session dossier and the secrets dialog already use. Focus trap, Escape,
// backdrop and focus restore all come with those two primitives, so nothing
// here re-implements any of it.
//
// It is a FILE OF ITS OWN rather than another export of Owners.jsx because of
// what it imports: the generic Sheet pulls in `preact/compat`, and a module
// that reaches compat cannot be loaded beside the palette's hook-runtime
// tests (bun's mock.module is process-wide). Owners.jsx is imported by those
// tests for `createFailure`; keeping the two surfaces apart keeps the form
// testable without a DOM.
export function NewOwnerDialog({ open, defaultDir = "", onCreate, onClose, phone = false }) {
  // The phone's model page (Owners.jsx `modelPageParent`): null is the form.
  // It lives here because the sheet's head and its Escape walk it back.
  const [modelView, setModelView] = useState(null);
  const close = () => { setModelView(null); onClose?.(); };
  const form = (
    <NewOwner
      defaultDir={defaultDir}
      phone={phone}
      modelView={modelView}
      onModelView={setModelView}
      /* The dialog leaves only once the owner EXISTS. A form that closes on a
         failed request loses both the failure and everything that was typed,
         so a refusal stays here, beside the button that caused it. */
      onCreate={async (spec) => { await onCreate?.(spec); close(); }}
    />
  );
  if (phone) {
    return (
      <MobileSheet
        open={open}
        onClose={close}
        title={modelPageTitle(modelView)}
        onBack={modelView == null ? undefined : () => setModelView(modelPageParent(modelView))}
        backLabel={modelPageBackLabel(modelView)}
      >
        {form}
      </MobileSheet>
    );
  }
  return (
    <Sheet open={open} onClose={onClose} title="New owner" class="ow-dialog">
      {form}
    </Sheet>
  );
}
