// VoiceMinutesBlock — the record a call leaves in the conversation it called:
// what was decided, what was already passed to this session, what is still
// open, and the transcript to download. Same grammar as the call block
// (glyph in the gutter, mono provenance line), open by default: it is the
// thing to read after hanging up. It is never a draft and never the user's.
import { useState } from "preact/hooks";
import { ChevronRight, AudioLines, FileText } from "lucide-preact";
import { eventAge } from "../EventBlock/EventBlock.jsx";
import { useStore } from "../../hooks/useStore.js";
import { callParty } from "../VoiceCall/VoiceCall.jsx";
import { parseVoiceMinutes, minutesTitle } from "../../data/voice-minutes.js";
import "./VoiceCallBlock.css";

export function minutesDownloadURL(attachment, sessionId) {
  if (!attachment?.attachment_id || !sessionId) return null;
  return `/api/sessions/${encodeURIComponent(sessionId)}/attachments/${encodeURIComponent(attachment.attachment_id)}`;
}

function Section({ title, tone, items, render = (x) => x }) {
  if (!items.length) return null;
  return (
    <div class={`vmb-sec is-${tone}`}>
      <h5 class="vmb-h">{title}</h5>
      <ul class="vmb-list">
        {items.map((item, i) => <li key={i}>{render(item)}</li>)}
      </ul>
    </div>
  );
}

export function VoiceMinutesBlock({ text, minutes: structured = null, attachments = [], time, sessionId, name = "this session" }) {
  const [open, setOpen] = useState(true);
  const minutes = parseVoiceMinutes(text, structured);
  const age = eventAge(time);
  const empty = !minutes.decided.length && !minutes.passed.length && !minutes.open.length;
  return (
    <section class={`vcb vmb${open ? " open" : ""}`}>
      <button type="button" class="vcb-head" aria-expanded={open} onClick={() => setOpen((v) => !v)}>
        <span class="vcb-id">
          <span class="vcb-meta">
            <span class="vcb-glyph" aria-hidden="true"><AudioLines size={13} /></span>
            <span class="vcb-source">voice call</span>
            {age && <span>· {age}</span>}
          </span>
          <span class="vcb-title">{minutesTitle(minutes, name)}</span>
        </span>
        <span class="vcb-chev" aria-hidden="true"><ChevronRight size={14} /></span>
      </button>
      {open && (
        <div class="vmb-body">
          {minutes.dropped && <p class="vmb-note">The call dropped before its minutes were written. The transcript has what was said.</p>}
          <Section title="Decided" tone="decided" items={minutes.decided} />
          <Section title={`Passed to ${name}`} tone="passed" items={minutes.passed} render={(p) => p.summary} />
          <Section title="Still open" tone="open" items={minutes.open} />
          {empty && !minutes.dropped && <p class="vmb-note">Nothing decided or left open.</p>}
          <div class="vmb-files">
            {attachments.map((a) => {
              const href = minutesDownloadURL(a, sessionId);
              return href ? (
                <a class="vmb-file" key={a.attachment_id} href={href} download={a.filename || "transcript.md"}>
                  <FileText size={13} aria-hidden="true" />{a.filename || "transcript.md"}
                </a>
              ) : null;
            })}
            {minutes.transcriptMissing && <span class="vmb-note">The transcript could not be saved.</span>}
          </div>
        </div>
      )}
    </section>
  );
}

// The record names who took the call the way the call screen did: the owner,
// else the session's title.
export function VoiceMinutesForSession(props) {
  const owners = useStore((s) => s.owners.list);
  const sessions = useStore((s) => s.sessions);
  return <VoiceMinutesBlock {...props} name={callParty(owners, sessions, props.sessionId).name} />;
}
