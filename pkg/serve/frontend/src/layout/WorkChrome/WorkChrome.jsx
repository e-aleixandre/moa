import { useEffect, useRef, useState } from "preact/hooks";
import { ChevronLeft, ChevronRight, Copy, Check, Square } from "lucide-preact";
import { copyToClipboard } from "../../data/util/format.js";
import "./WorkChrome.css";

// WorkChrome — the frame the two OPEN-WORK screens share: a delegated
// subagent and a background command. They share a door home, a state word, a
// stop control and an audit disclosure; they do NOT share a semantic template,
// because an errand has a report and a process has an exit.
//
// Everything here is deliberately one module with one stylesheet: the same
// four call sites (desktop/phone × subagent/bash) used to reimplement a header
// each, which is how the phone ended up with a different back affordance and a
// different state vocabulary than the desktop.

// WorkHead — one region, one hairline, and the title is THE WORK: the errand
// for a subagent, the command for a process. The agent/model line is what
// truncates first; the title never does.
//
// `copyLabel` makes the title itself the copy target, which is what a command
// needs — it is the one thing on the screen you take away verbatim, and a 14px
// icon next to 76 characters of shell is the worse target of the two. An
// errand is prose, is not copied, and stays a heading.
export function WorkHead({ phone, parent, onBack, title, titleMono, copyLabel, sub, state, actions }) {
  const [copied, setCopied] = useState(false);
  useEffect(() => {
    if (!copied) return undefined;
    const t = setTimeout(() => setCopied(false), 1200);
    return () => clearTimeout(t);
  }, [copied]);
  const cls = `wk-title${titleMono ? " is-mono" : ""}`;
  return (
    <header class={`wk-head${phone ? " is-phone" : ""}`}>
      <div class="wk-head-top">
        {/* ONE door out, and it says where it leads. Desktop prints the
            parent's own name; the phone prints "Parent", because 390px cannot
            spend 22 characters on the way back without shortening the title.
            Both carry the same full accessible name. */}
        <button
          type="button"
          class={`wk-home${phone ? " is-phone" : ""}`}
          onClick={onBack}
          aria-label={`Back to ${parent}`}
        >
          <ChevronLeft size={16} aria-hidden="true" />
          <span class="wk-home-t">{phone ? "Parent" : parent}</span>
        </button>
        {state}
        <span class="wk-sp" />
        {actions}
      </div>
      {copyLabel ? (
        <button
          type="button"
          class={`${cls} is-copy${phone ? " is-phone" : ""}`}
          aria-label={copied ? "Copied" : copyLabel}
          onClick={() => copyToClipboard(String(title || "")).then((ok) => ok && setCopied(true))}
        >
          <span class="wk-title-t">{title}</span>
          {copied ? <Check size={14} aria-hidden="true" /> : <Copy size={14} aria-hidden="true" />}
        </button>
      ) : (
        <h2 class={cls}>{title}</h2>
      )}
      {sub && <p class="wk-sub">{sub}</p>}
    </header>
  );
}

// StateWord — the state, and the ONE number allowed to travel with it.
// Terminal states carry no dot: a finished run has no pulse, and a green tick
// on "Completed" would spend the running colour on something that has stopped.
// Green breathes; amber, when a backend ever emits it, does not move at all.
export function StateWord({ tone, word, time }) {
  const dot = tone === "running" || tone === "waiting";
  return (
    <span class={`wk-state is-${tone}`}>
      {dot && <span class={`wk-dot is-${tone}`} aria-hidden="true" />}
      <span class="wk-state-w">{word}</span>
      {time && <span class="wk-state-t">{time}</span>}
    </span>
  );
}

// StopButton — two steps, and red only once it is asking. It disappears rather
// than sitting disabled when there is nothing left to stop.
export function StopButton({ phone, armed, onStop, busyLabel }) {
  if (busyLabel) return <span class="wk-stopping">{busyLabel}</span>;
  return (
    <button
      type="button"
      class={`wk-stop${armed ? " is-armed" : ""}${phone ? " is-phone" : ""}`}
      onClick={onStop}
      aria-label={armed ? "Confirm stop" : "Stop"}
    >
      <Square size={11} fill="currentColor" aria-hidden="true" />
      <span>{armed ? "sure?" : "Stop"}</span>
    </button>
  );
}

// CopyAction — a secondary button that copies one thing and says it did.
export function CopyAction({ phone, text, label }) {
  const [copied, setCopied] = useState(false);
  useEffect(() => {
    if (!copied) return undefined;
    const t = setTimeout(() => setCopied(false), 1200);
    return () => clearTimeout(t);
  }, [copied]);
  return (
    <button
      type="button"
      class={`wk-btn${phone ? " is-phone" : ""}`}
      onClick={() => copyToClipboard(String(text || "")).then((ok) => ok && setCopied(true))}
    >
      {copied ? <Check size={13} aria-hidden="true" /> : <Copy size={13} aria-hidden="true" />}
      {copied ? "Copied" : label}
    </button>
  );
}

// Disclosure — closed by default on a finished run: the record is evidence,
// and evidence is what you open when the report is not enough. It never loses
// anything; the transcript and the ledger are inside.
export function Disclosure({ label, count, openInit = false, onToggle, children }) {
  const [open, setOpen] = useState(openInit);
  const ref = useRef(null);
  const toggle = () => {
    const next = !open;
    setOpen(next);
    if (onToggle) onToggle(next, ref.current);
  };
  return (
    <div class={`wk-disc${open ? " is-open" : ""}`} ref={ref}>
      <button type="button" class="wk-disc-b" onClick={toggle} aria-expanded={open}>
        <span class={`wk-chev${open ? " is-open" : ""}`} aria-hidden="true">
          <ChevronRight size={14} />
        </span>
        <span class="wk-disc-t">{label}</span>
        {count && <span class="wk-disc-n">{count}</span>}
      </button>
      {open && <div class="wk-disc-body">{children}</div>}
    </div>
  );
}

// RunDetails — the audit, at the end, where audit belongs. Printed rows, not
// boxes (boxes are for what you press) except the identifiers, which you press
// to copy and therefore look pressable.
export function RunDetails({ phone, rows = [], ids = [] }) {
  return (
    <Disclosure label="Run details">
      {rows.length > 0 && (
        <dl class="wk-rd">
          {rows.map(([k, v]) => (
            <div key={k}>
              <dt>{k}</dt>
              <dd>{v}</dd>
            </div>
          ))}
        </dl>
      )}
      {ids.length > 0 && (
        <div class="wk-ids">
          {ids.map(([k, v]) => <IdRow key={k} label={k} value={v} phone={phone} />)}
        </div>
      )}
    </Disclosure>
  );
}

function IdRow({ label, value, phone }) {
  const [copied, setCopied] = useState(false);
  useEffect(() => {
    if (!copied) return undefined;
    const t = setTimeout(() => setCopied(false), 1200);
    return () => clearTimeout(t);
  }, [copied]);
  return (
    <button
      type="button"
      class={`wk-id${phone ? " is-phone" : ""}`}
      aria-label={`Copy ${label}`}
      onClick={() => copyToClipboard(String(value || "")).then((ok) => ok && setCopied(true))}
    >
      <span class="wk-id-k">{label}</span>
      <span class="wk-id-v">{value}</span>
      {copied ? <Check size={13} aria-hidden="true" /> : <Copy size={13} aria-hidden="true" />}
    </button>
  );
}
