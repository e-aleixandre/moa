import { useState } from "preact/hooks";
import { Field } from "../../primitives/Field/Field.jsx";
import { useStore } from "../../hooks/useStore.js";
import { OwnerAvatarFor } from "../Owners/OwnerAvatar.jsx";
import { CallLevelField } from "../Owners/CallLevel.jsx";
import { CALL_LEVEL_HELP } from "../../data/owner-calls.js";
import { ownerState } from "../../data/owners-model.js";
import {
  PAUSE_OPTIONS, RING_DEFAULT, RING_MAX, RING_MIN, callsPatch, deviceTimeZone, hoursBlock,
  pauseUntil, pausedLabel, pausedNow, ringSecondsValue,
} from "../../data/owner-calls.js";
import "./OwnerCallsSettings.css";

/* Calls from owners — who may ask to call you, and when. The server replaces
   the whole calls block on every save, so each control sends the rest as it
   is (callsPatch). No daily cap: hours and a pause are the only limits. */

function Hours({ calls, save, loaded }) {
  const saved = calls.hours || null;
  const [between, setBetween] = useState(!!saved);
  const [from, setFrom] = useState(saved?.from || "08:00");
  const [until, setUntil] = useState(saved?.until || "22:00");
  const [error, setError] = useState("");
  const tz = saved?.tz || deviceTimeZone();
  const commit = (nextFrom, nextUntil) => {
    const r = hoursBlock({ from: nextFrom, until: nextUntil });
    if (r.error) { setError(r.error); return; }
    setError("");
    save(callsPatch(calls, { hours: r.hours }));
  };
  return (
    <>
      <span class="zl-set-k is-page">Allowed hours</span>
      <div class="oc-seg" role="radiogroup" aria-label="Allowed hours">
        <button type="button" role="radio" aria-checked={!between} class={`oc-opt${!between ? " is-on" : ""}`} disabled={!loaded}
          onClick={() => { setBetween(false); setError(""); if (saved) save(callsPatch(calls, { hours: null })); }}>
          Any time
        </button>
        <button type="button" role="radio" aria-checked={between} class={`oc-opt${between ? " is-on" : ""}`} disabled={!loaded}
          onClick={() => { setBetween(true); commit(from, until); }}>
          From – Until
        </button>
      </div>
      {between && (
        <div class="oc-hours">
          <label class="oc-time">
            <span>From</span>
            <Field variant="box" type="time" value={from} aria-label="Calls allowed from"
              onInput={(e) => { setFrom(e.currentTarget.value); commit(e.currentTarget.value, until); }} />
          </label>
          <label class="oc-time">
            <span>Until</span>
            <Field variant="box" type="time" value={until} aria-label="Calls allowed until"
              onInput={(e) => { setUntil(e.currentTarget.value); commit(from, e.currentTarget.value); }} />
          </label>
          <span class="oc-tz zl-data">{tz}</span>
        </div>
      )}
      {error && <p class="oc-fail" role="alert">{error}</p>}
    </>
  );
}

function Pause({ calls, save, loaded }) {
  const paused = pausedNow(calls);
  return (
    <>
      <span class="zl-set-k is-page">Pause calls</span>
      {paused ? (
        <div class="oc-paused">
          <span class="oc-paused-t">{pausedLabel(calls)}</span>
          <button type="button" class="oc-btn" onClick={() => save(callsPatch(calls, { paused_until: "" }))}>Resume</button>
        </div>
      ) : (
        <div class="oc-pause" role="group" aria-label="Pause calls">
          {PAUSE_OPTIONS.map((o) => (
            <button key={o.id} type="button" class="oc-btn" disabled={!loaded}
              onClick={() => save(callsPatch(calls, { paused_until: pauseUntil(o.id) }))}>
              {o.label}
            </button>
          ))}
        </div>
      )}
    </>
  );
}

function RingFor({ calls, save, loaded }) {
  const saved = calls.ring_seconds || RING_DEFAULT;
  const [draft, setDraft] = useState(String(saved));
  const [error, setError] = useState("");
  const commit = () => {
    const r = ringSecondsValue(draft);
    if (r.error) { setError(r.error); return; }
    setError("");
    if (r.seconds !== saved) save(callsPatch(calls, { ring_seconds: r.seconds }));
  };
  return (
    <>
      <span class="zl-set-k is-page">Ring for</span>
      <label class="oc-ring">
        <Field variant="box" class="oc-ring-f" type="number" inputmode="numeric" min={RING_MIN} max={RING_MAX} value={draft} disabled={!loaded}
          aria-label="Ring for, in seconds"
          onInput={(e) => setDraft(e.currentTarget.value)} onBlur={commit}
          onKeyDown={(e) => { if (e.key === "Enter") commit(); }} />
        <span>seconds</span>
      </label>
      {error && <p class="oc-fail" role="alert">{error}</p>}
    </>
  );
}

export function OwnerCallsSettings({ state, onProviders }) {
  const { status, loaded, save } = state;
  const owners = useStore((s) => s.owners.list);
  const calls = status?.calls || {};
  return (
    <>
      <span class="zl-set-k is-page">Calls from owners</span>
      {owners.length === 0 ? (
        <p class="zl-set-empty">No owners yet.</p>
      ) : (
        <>
        <p class="ow-hint oc-help">{CALL_LEVEL_HELP}</p>
        <ul class="oc-owners">
          {owners.map((o) => (
            <li key={o.id} class="oc-owner">
              <div class="oc-owner-h">
                <OwnerAvatarFor owner={o} state={ownerState(o)} size={24} />
                <span class="oc-owner-n">{o.name}</span>
              </div>
              <CallLevelField owner={o} status={status} onProviders={onProviders} label={false} hint={false} />
            </li>
          ))}
        </ul>
        </>
      )}
      {loaded && status && (
        <>
          <Hours calls={calls} save={save} loaded />
          <Pause calls={calls} save={save} loaded />
          <RingFor calls={calls} save={save} loaded />
        </>
      )}
    </>
  );
}
