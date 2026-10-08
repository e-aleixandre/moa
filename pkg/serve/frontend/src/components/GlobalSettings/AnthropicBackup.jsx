import { useEffect, useRef, useState } from "preact/hooks";
import { Field } from "../../primitives/index.js";
import { api } from "../../data/api.js";
import { enableAnthropicBackup, removeAnthropicBackup, saveAnthropicBackup } from "./providers-flow.js";

export function AnthropicBackup({ backup, onChanged }) {
  const [step, setStep] = useState("");
  const [draft, setDraft] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const input = useRef(null);
  const live = useRef(true);
  const pending = useRef(false);
  useEffect(() => () => { live.current = false; if (input.current) input.current.value = ""; }, []);
  const cancel = () => { setDraft(""); if (input.current) input.current.value = ""; setStep(""); setError(""); };
  const act = async (request) => {
    if (pending.current) return;
    pending.current = true; setBusy(true); setError("");
    try {
      const row = await request();
      if (live.current) { cancel(); onChanged?.(row); }
    } catch {
      if (live.current) { setError("Couldn't save the change. Reload Providers and try again."); onChanged?.(); }
    } finally { pending.current = false; if (live.current) setBusy(false); }
  };
  const save = (event) => {
    event.preventDefault();
    const key = draft;
    setDraft(""); if (input.current) input.current.value = "";
    act(() => saveAnthropicBackup(api, key, backup.revision));
  };
  const reading = backup.enabled ? "Enabled for plan limits" : backup.configured ? "Stored · off" : "Not configured";
  return <div class="zl-prov-flow" aria-label="Anthropic API backup">
    <span class="zl-prov-label">API backup · {reading}</span>
    {backup.state === "primary_changed" && <p class="zl-prov-step is-warn">Enable again to associate the stored backup with this sign-in.</p>}
    {backup.state === "api_error" && <p class="zl-prov-step is-warn">Check the API error in your session, or replace the backup key. Your OAuth sign-in is unchanged.</p>}
    {backup.state === "save_failed" && <p class="zl-prov-step is-warn">Retry the change. Backup dispatches are blocked until it is saved.</p>}
    {step === "key" && <form noValidate autocomplete="off" onSubmit={save}>
      <label class="zl-prov-label" for="zl-anthropic-backup-key">Backup API key</label>
      <Field id="zl-anthropic-backup-key" type="password" size="lg" mono inputRef={input} value={draft} autocomplete="off" autoCorrect="off" autocapitalize="off" spellcheck={false} data-1p-ignore="true" data-lpignore="true" disabled={busy} onInput={(e) => setDraft(e.currentTarget.value)} />
      <p class="zl-prov-step">Save without changing your OAuth sign-in. Then enable separately.</p>
      <div class="zl-prov-acts"><button class="zl-prov-btn is-primary" type="submit" disabled={busy || !draft.trim()}>Save backup</button><button class="zl-prov-btn" type="button" onClick={cancel}>Cancel</button></div>
    </form>}
    {step === "enable" && <>
      <p class="zl-prov-step">Enable for sessions and subagents when OAuth confirms a 5h or weekly plan limit. API billing follows Anthropic's credits and billing settings; if it refuses the request, you'll see the error.</p>
      <p class="zl-prov-step">OAuth stays signed in and is preferred on the next compatible natural request. Anthropic may discard signed thinking from another account; text and tool results are kept.</p>
      <div class="zl-prov-acts"><button type="button" class="zl-prov-btn is-primary" disabled={busy} onClick={() => act(() => enableAnthropicBackup(api, true, backup.revision))}>Enable API backup</button><button type="button" class="zl-prov-btn" onClick={cancel}>Cancel</button></div>
    </>}
    {!step && <div class="zl-prov-acts">
      <button type="button" class="zl-prov-btn" disabled={busy} onClick={() => setStep("key")}>{backup.configured ? "Replace backup key" : "Add backup key"}</button>
      {backup.configured && <button type="button" class="zl-prov-btn" disabled={busy || !backup.eligible} onClick={() => backup.enabled ? act(() => enableAnthropicBackup(api, false, backup.revision)) : setStep("enable")}>{backup.enabled ? "Disable backup" : "Enable backup…"}</button>}
      {backup.configured && <button type="button" class="zl-prov-btn" disabled={busy} onClick={() => act(() => removeAnthropicBackup(api, backup.revision))}>Remove backup</button>}
    </div>}
    {error && <p class="zl-prov-msg is-warn" role="alert">{error}</p>}
  </div>;
}
