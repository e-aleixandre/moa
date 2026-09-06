import { Button } from "../../primitives/index.js";

// The two questions Live Preview ever asks, as plain components: no hooks, no
// state of their own. They exist apart from the panel because they are the
// whole first-run experience and deserve to be exercised on their own.

// PreviewURLSetup — first run (and "Change URL"): the ONLY moment the app URL is
// worth a screen. It takes the stage instead of a permanent row, so once the app
// is loaded those pixels go back to it for good.
export function PreviewURLSetup({ value, onInput, onCommit, onCancel, canCancel }) {
  return (
    <div class="live-preview-setup">
      <p class="live-preview-setup-title">Enter your app URL</p>
      <div class="live-preview-setup-row">
        <input
          class="live-preview-url"
          type="url"
          inputMode="url"
          autocapitalize="off"
          autocorrect="off"
          spellcheck={false}
          placeholder="http://localhost:5173"
          value={value}
          autofocus
          onInput={(e) => onInput(e.currentTarget.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") onCommit();
            if (e.key === "Escape" && canCancel) onCancel();
          }}
          aria-label="Preview URL"
        />
        <Button variant="solid" size="sm" onClick={onCommit} disabled={!String(value || "").trim()}>
          Load
        </Button>
      </div>
      <p class="live-preview-setup-hint">Enter your development server URL.</p>
    </div>
  );
}

// PreviewAddressSetup — asked once, then remembered. Moa opens the proxy port
// itself, but only the user knows how their browser reaches that machine (a
// tailnet name, a reverse proxy, a LAN address), so the field arrives filled in
// with the host they are already on and they confirm it or fix the port.
export function PreviewAddressSetup({ value, onInput, onCommit, onBack, error }) {
  return (
    <div class="live-preview-setup">
      <p class="live-preview-setup-title">Confirm the preview address</p>
      <div class="live-preview-setup-row">
        <input
          class="live-preview-url"
          type="url"
          inputMode="url"
          autocapitalize="off"
          autocorrect="off"
          spellcheck={false}
          placeholder="https://your-host:7492"
          value={value}
          autofocus
          onInput={(e) => onInput(e.currentTarget.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") onCommit();
          }}
          aria-label="Preview proxy address"
        />
        <Button variant="solid" size="sm" onClick={onCommit} disabled={!String(value || "").trim()}>
          Start
        </Button>
      </div>
      <p class="live-preview-setup-hint">
        Moa opens this port on the machine it runs on. Change it if you reach that
        machine at a different address.
      </p>
      {error && <p class="live-preview-setup-error" role="alert">{error}</p>}
      <button type="button" class="live-preview-setup-back" onClick={onBack}>
        Change the app URL
      </button>
    </div>
  );
}

// PreviewErrorBanner — an open socket is not a working preview. When the frame
// cannot load through the public origin, saying so is not enough: the address is
// corrected from the same place the problem is reported.
export function PreviewErrorBanner({ message, onChangeAddress }) {
  return (
    <div class="live-preview-proxy-error" role="alert">
      <span>{message}</span>
      <button type="button" class="live-preview-proxy-error-action" onClick={onChangeAddress}>
        Change the preview address
      </button>
    </div>
  );
}
