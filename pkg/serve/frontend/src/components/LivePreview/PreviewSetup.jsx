import { Field, Spinner } from "../../primitives/index.js";

// The questions and the states Live Preview shows INSTEAD of the app, as plain
// components: no hooks, no state of their own. They all speak one grammar, the
// ambient card: a sheet-coloured surface lit by its rim, a 16px title that says
// what to do, and at most one accent — the action that moves forward. Anything
// that would explain how the preview works is left out; the field's placeholder
// already shows the shape of the answer.

// SetupCard — the one surface every non-app state of the stage uses.
function SetupCard({ title, children, tone }) {
  return (
    <div class="live-preview-setup">
      <div class={`live-preview-card${tone ? ` is-${tone}` : ""}`}>
        <p class="live-preview-setup-title">{title}</p>
        {children}
      </div>
    </div>
  );
}

function GoButton({ onClick, disabled, children }) {
  return (
    <button type="button" class="live-preview-go" onClick={() => onClick()} disabled={disabled}>
      {children}
    </button>
  );
}

// PreviewURLSetup — first run (and "Change URL"). `recent` are addresses this
// browser already previewed in other sessions: on a phone, one tap instead of
// typing "localhost:5173" on a keyboard without a colon key.
export function PreviewURLSetup({ value, onInput, onCommit, onCancel, canCancel, recent = [], onPick, inputRef }) {
  const empty = !String(value || "").trim();
  return (
    <SetupCard title="Open your app">
      <div class="live-preview-setup-row">
        <Field
          variant="box"
          size="lg"
          mono
          class="live-preview-url"
          type="url"
          inputMode="url"
          autocapitalize="off"
          autoCorrect="off"
          spellcheck={false}
          placeholder="localhost:5173"
          value={value}
          inputRef={inputRef}
          autofocus
          onInput={(e) => onInput(e.currentTarget.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") onCommit();
            if (e.key === "Escape" && canCancel) onCancel();
          }}
          aria-label="Preview URL"
        />
        <GoButton onClick={onCommit} disabled={empty}>Open</GoButton>
      </div>
      {recent.length > 0 && (
        <div class="live-preview-recent" role="group" aria-label="Open again">
          {recent.map((url) => (
            <button key={url} type="button" class="live-preview-recent-item" onClick={() => onPick?.(url)}>
              {displayURL(url)}
            </button>
          ))}
        </div>
      )}
      {canCancel && (
        <button type="button" class="live-preview-setup-back" onClick={onCancel}>
          Cancel
        </button>
      )}
    </SetupCard>
  );
}

// PreviewErrorBanner — the proxy did not start, so there is no app to cover:
// the failure takes the stage, in the same card, with the fix as its action.
export function PreviewErrorBanner({ message, onRetry, onChangeURL }) {
  return (
    <SetupCard title="The preview didn’t start" tone="error">
      <p class="live-preview-setup-hint" role="alert">{message}</p>
      <div class="live-preview-setup-actions">
        <button type="button" class="live-preview-go live-preview-proxy-error-action" onClick={onRetry}>
          Try again
        </button>
        <button type="button" class="live-preview-setup-back" onClick={onChangeURL}>
          Change the app URL
        </button>
      </div>
    </SetupCard>
  );
}

// PreviewLoading — between "Open" and the app's first paint. Without it the
// stage is a blank rectangle and a slow dev server looks exactly like a broken
// one.
export function PreviewLoading({ url }) {
  return (
    <div class="live-preview-loading" role="status">
      <Spinner color="overlay1" size={14} />
      <span class="live-preview-loading-url">{url ? displayURL(url) : "Starting the preview"}</span>
    </div>
  );
}

// PreviewRecoveryNotice — floats over the app instead of pushing it down: the
// page is still there and still usable, only Moa's hold on it is gone.
export function PreviewRecoveryNotice({ message, onReturn }) {
  return (
    <div class="live-preview-recovery" role="alert">
      <span class="live-preview-recovery-dot" aria-hidden="true" />
      <span class="live-preview-recovery-text">{message}</span>
      <button type="button" class="live-preview-recovery-action" onClick={onReturn}>
        Return to app
      </button>
    </div>
  );
}

// displayURL — what a person reads: no scheme, no trailing slash.
export function displayURL(url) {
  return String(url || "").replace(/^https?:\/\//i, "").replace(/\/$/, "");
}
