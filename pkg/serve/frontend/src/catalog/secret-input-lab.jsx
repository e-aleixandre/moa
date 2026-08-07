import { Lock, Plus, X } from "lucide-preact";
import "./responsive-lab.css";
import "./secret-input-lab.css";

// Catalog-only fixtures. The stateful Composer is deliberately not mounted:
// its draft and history persistence is the boundary this exploration examines.

const WIDTHS = [
  { label: "320", width: 320, note: "narrow phone" },
  { label: "375", width: 375, note: "phone" },
  { label: "desktop", width: 720, note: "wide conversation" },
];

const ALIASES = ["db-produccion", "api-key", "smtp-pass"];

const VARIANTS = [
  {
    id: "sheet-command",
    label: "A · Command-opened sheet",
    note: "An intentional /secret command opens one batch sheet with no persistent new chrome. Command users get aliases pre-labelled; /secret alone starts one self-named row. The trade-off is that the entry point remains invisible to people who do not know the command.",
  },
  {
    id: "composer-lock",
    label: "B · Composer lock affordance",
    note: "The same batch sheet is discoverable from the composer, but the lock joins attachment and send in the already crowded mobile action row. It is visible without learning a command, at the cost of a tighter familiar control cluster.",
  },
  {
    id: "inline-masked",
    label: "C · Inline masked mode",
    note: "The composer becomes the batch form in place. Three stacked values keep their aliases visible, but at 320px they consume most of the composer and push ordinary controls below the fields; this is intentionally shown as the constrained case, not a compact success.",
  },
];

function ActionRow({ lock = false, save = false }) {
  return (
    <div class="secret-lab-action-row">
      <button type="button" class="secret-lab-action" aria-label="Attach" tabIndex={-1}>+</button>
      {lock && (
        <button type="button" class="secret-lab-action secret-lab-lock" aria-label="Send secret batch" tabIndex={-1}>
          <Lock size={15} strokeWidth={2} aria-hidden="true" />
        </button>
      )}
      <div class="secret-lab-compose-copy">{save ? "Masked batch mode" : "Message moa"}</div>
      <button type="button" class={`secret-lab-send ${save ? "secret-lab-store" : ""}`} tabIndex={-1}>
        {save ? "Send" : "↑"}
      </button>
    </div>
  );
}

function SecretRow({ alias, selfNamed = false }) {
  return (
    <div class="secret-lab-secret-row">
      {selfNamed ? (
        <label class="secret-lab-field secret-lab-alias-field">
          <span>Alias</span>
          <input type="text" placeholder="e.g. db-produccion" readOnly />
        </label>
      ) : <span class="secret-lab-row-alias">{alias}</span>}
      <label class="secret-lab-field secret-lab-value-field">
        <span class="secret-lab-sr-only">Secret value for {alias || "new secret"}</span>
        <input type="password" placeholder="Secret value" readOnly aria-label={`Secret value for ${alias || "new secret"}`} />
      </label>
      <button type="button" class="secret-lab-remove" aria-label={`Remove ${alias || "new secret"}`} tabIndex={-1}>
        <X size={14} strokeWidth={2} aria-hidden="true" />
      </button>
    </div>
  );
}

function SecretSheet({ selfNamed = false }) {
  const aliases = selfNamed ? [""] : ALIASES;
  return (
    <div class="secret-lab-sheet" role="group" aria-label="Send secret batch">
      <div class="secret-lab-sheet-grab" aria-hidden="true" />
      <div class="secret-lab-sheet-title"><h4>Send secrets</h4><span>Batch</span></div>
      <div class="secret-lab-rows">
        {aliases.map((alias, index) => <SecretRow key={alias || index} alias={alias} selfNamed={selfNamed} />)}
      </div>
      <button type="button" class="secret-lab-add" tabIndex={-1}><Plus size={14} aria-hidden="true" /> Add another</button>
      <p class="secret-lab-hint">One 0600 file per alias in one temp directory. Moa does not put values in chat input.</p>
      <div class="secret-lab-sheet-actions">
        <button type="button" class="secret-lab-cancel" tabIndex={-1}>Cancel</button>
        <button type="button" class="secret-lab-save" tabIndex={-1}>Send</button>
      </div>
    </div>
  );
}

function InlineBatch() {
  return (
    <div class="secret-lab-inline" role="group" aria-label="Masked secret batch mode">
      <div class="secret-lab-inline-heading"><Lock size={14} aria-hidden="true" /> Masked batch mode</div>
      {ALIASES.map((alias) => <SecretRow key={alias} alias={alias} />)}
      <button type="button" class="secret-lab-add" tabIndex={-1}><Plus size={14} aria-hidden="true" /> Add another</button>
      <ActionRow save />
    </div>
  );
}

function Frame({ variant, selfNamed = false }) {
  if (variant.id === "inline-masked") {
    return (
      <div class={`secret-lab-frame secret-lab--${variant.id}`}>
        <div class="secret-lab-message" aria-hidden="true"><span /><span /><span /></div>
        <InlineBatch />
      </div>
    );
  }
  const command = variant.id === "sheet-command";
  return (
    <div class={`secret-lab-frame secret-lab--${variant.id}`}>
      <div class="secret-lab-message" aria-hidden="true"><span /><span /><span /></div>
      <div class="secret-lab-composer">
        <div class="secret-lab-command">{command ? (selfNamed ? "/secret" : "/secret db-produccion api-key smtp-pass") : "Message moa"}</div>
        <ActionRow lock={!command} />
      </div>
      <SecretSheet selfNamed={selfNamed} />
    </div>
  );
}

function Specimen({ variant, width, selfNamed = false }) {
  return (
    <article class="responsive-specimen secret-lab-specimen" style={{ width: `${width.width}px` }}>
      <header class="responsive-specimen-head"><b>{width.label}px</b><span>{selfNamed ? "zero-argument command" : width.note}</span></header>
      <Frame variant={variant} selfNamed={selfNamed} />
    </article>
  );
}

function TranscriptCard() {
  return (
    <>
      <article class="secret-lab-transcript-card">
        <span class="secret-lab-transcript-lock" aria-hidden="true">🔐</span>
        <div>
          <b>db-produccion · api-key · smtp-pass</b>
          <p>3 secrets staged · Moa sends the model only the directory path and aliases</p>
        </div>
      </article>
      <pre class="secret-lab-agent-message">3 secrets are available in /tmp/moa-secret-a3f9/ (db-produccion, api-key, smtp-pass). Install each where it belongs and delete the files.</pre>
    </>
  );
}

export function SecretInputLab() {
  return (
    <section class="responsive-lab secret-input-lab">
      <h2>Secret input boundary</h2>
      <p>
        A dedicated masked batch entry keeps values out of chat input and your persisted message; Moa sends the model only aliases and one directory path. It is not a vault or a boundary against the agent: its shell runs as the same Unix user and can read the files. If it does, a value enters context and the transcript as tool output.
      </p>
      {VARIANTS.map((variant) => (
        <section class="responsive-lab-state" key={variant.id}>
          <h3>{variant.label}</h3>
          <p class="secret-lab-note">{variant.note}</p>
          <div class="responsive-specimens">
            {WIDTHS.map((width) => <Specimen key={width.label} variant={variant} width={width} />)}
          </div>
          {variant.id === "sheet-command" && (
            <>
              <p class="secret-lab-subnote">/secret with no aliases opens the same sheet with one empty, self-named row.</p>
              <div class="responsive-specimens">
                {WIDTHS.map((width) => <Specimen key={width.label} variant={variant} width={width} selfNamed />)}
              </div>
            </>
          )}
        </section>
      ))}
      <section class="responsive-lab-state">
        <h3>Shared transcript result</h3>
        <p class="secret-lab-note">One batch creates one directory and one context message. The staged note records aliases and has no value or reveal control.</p>
        <TranscriptCard />
      </section>
    </section>
  );
}
