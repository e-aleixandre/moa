// voice-minutes.js — reads the minutes record a call leaves in the
// conversation (custom.source "voice_call", kind "minutes"). The server
// writes it once, at hangup, as display-only text in a fixed shape; this
// turns that text into the block's sections. It is never a draft.

import { PROVIDER_NAMES } from './voice-call.js';

const HEADER = /^Voice call ended \(([^)]*)\) after (\d+)s with ([\w-]+), billing ([\w-]+)\./;
const SECTIONS = [
  ['decided', /^Decided:$/],
  ['passed', /^Passed to this session\b.*:$/],
  ['open', /^Still open:$/],
];

export function parseVoiceMinutes(text) {
  const out = {
    reason: '', seconds: null, provider: '', billing: '', dropped: false,
    decided: [], passed: [], open: [], transcriptMissing: false,
  };
  const body = String(text || '');
  let section = null;
  for (const raw of body.split('\n')) {
    const line = raw.trim();
    if (!line || line === '<voice_minutes>' || line === '</voice_minutes>') continue;
    const head = HEADER.exec(line);
    if (head) {
      out.reason = head[1];
      out.seconds = Number(head[2]);
      out.provider = head[3];
      out.billing = head[4];
      continue;
    }
    if (line.startsWith('The call dropped before')) { out.dropped = true; continue; }
    if (line.startsWith('The transcript could not be saved')) { out.transcriptMissing = true; continue; }
    const named = SECTIONS.find(([, re]) => re.test(line));
    if (named) { section = named[0]; continue; }
    if (section && line.startsWith('- ')) {
      const item = line.slice(2).trim();
      if (item === '(none)') continue;
      if (section === 'passed') {
        const m = /^\[([^\]]+)\] already_delivered: (.*)$/.exec(item);
        out.passed.push(m ? { deliveryId: m[1], summary: m[2] } : { deliveryId: '', summary: item });
      } else {
        out[section].push(item);
      }
    }
  }
  return out;
}

function duration(seconds) {
  if (!Number.isFinite(seconds)) return '';
  return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, '0')}`;
}

// minutesTitle is the block's one line: who, how long, how it was paid.
export function minutesTitle(minutes, name) {
  const parts = [minutes.dropped ? `Call with ${name} dropped` : `Call with ${name}`];
  const d = duration(minutes.seconds);
  if (d) parts.push(d);
  if (minutes.provider) parts.push(PROVIDER_NAMES[minutes.provider] || minutes.provider);
  return parts.join(' · ');
}
