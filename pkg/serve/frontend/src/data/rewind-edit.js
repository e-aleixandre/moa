// rewind-edit.js — what rewinding one of your own messages hands back to the
// composer. The branch itself is the server's (POST /branch with before:true);
// this is the client half: bring the attachments back as Files and leave the
// text and files where the composer picks them up (store.composerDrops, the
// handoff a share and Stop's recall already use).

import { setState } from './store.js';
import { appendSharedText } from './share-target.js';

function inlineFile(a) {
  const bytes = Uint8Array.from(atob(a.data), (c) => c.charCodeAt(0));
  return new File([bytes], a.filename || 'attachment', { type: a.mime_type || 'application/octet-stream' });
}

// rewoundFiles returns the attachments as Files plus the names of those that
// could not come back (a blob the store no longer has, a network error). One
// missing blob never stops the rest: the message is still worth editing.
export async function rewoundFiles(sessionId, attachments, { fetch: fetchImpl = (...args) => globalThis.fetch(...args) } = {}) {
  const files = [];
  const missing = [];
  for (const a of attachments || []) {
    const label = a?.filename || a?.type || 'attachment';
    try {
      if (a?.data) {
        files.push(inlineFile(a));
        continue;
      }
      if (!a?.attachment_id) throw new Error('no blob');
      const res = await fetchImpl(`/api/sessions/${encodeURIComponent(sessionId)}/attachments/${encodeURIComponent(a.attachment_id)}`);
      if (!res.ok) throw new Error(String(res.status));
      const blob = await res.blob();
      files.push(new File([blob], a.filename || 'attachment', { type: a.mime_type || blob.type || 'application/octet-stream' }));
    } catch (_) {
      missing.push(label);
    }
  }
  return { files, missing };
}

// placeRewoundMessage leaves the message for the session's composer. Anything
// already waiting there (or already typed — the composer appends) stays: a
// rewind never costs you a draft.
export function placeRewoundMessage(sessionId, { text, files, mentions }) {
  setState((state) => {
    const prev = state.composerDrops[sessionId];
    return {
      composerDrops: {
        ...state.composerDrops,
        [sessionId]: {
          id: `rewind-${Date.now()}`,
          text: appendSharedText(prev?.text || '', text || ''),
          files: [...(prev?.files || []), ...(files || [])],
          mentions: [...(prev?.mentions || []), ...(mentions || [])],
          focus: true,
        },
      },
    };
  });
}
