// native-share.js — move the oldest retained iOS share into the ordinary
// destination picker. The native bridge is injected at document start because
// the container navigates from its local pairing page to this remote frontend;
// a Capacitor JS plugin imported only by the local page would not cross that
// origin change.

import { store } from './store.js';
import { addToast } from './notifications.js';
import { presentIncomingShare } from './share.js';

const NATIVE_ID = /^[a-f0-9]{8}-[a-f0-9]{4}-[1-5][a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$/i;
const MAX_FILES = 8;
const MAX_BYTES = 32 * 1024 * 1024;

function decodeFile(file) {
  if (!file || typeof file.data !== 'string' || typeof file.name !== 'string') {
    throw new Error('A shared file was incomplete.');
  }
  const binary = atob(file.data);
  if (binary.length !== Number(file.size) || binary.length > MAX_BYTES) {
    throw new Error('A shared file was incomplete.');
  }
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i += 1) bytes[i] = binary.charCodeAt(i);
  return new File([bytes], file.name || 'shared-file', {
    type: typeof file.mime === 'string' ? file.mime : 'application/octet-stream',
  });
}

export function decodeNativeShare(value) {
  if (!value || !NATIVE_ID.test(value.id || '')) throw new Error('The pending share has an invalid id.');
  const encodedFiles = Array.isArray(value.files) ? value.files : [];
  if (encodedFiles.length > MAX_FILES) throw new Error('The pending share has too many files.');
  let total = 0;
  const files = encodedFiles.map((entry) => {
    const file = decodeFile(entry);
    total += file.size;
    if (total > MAX_BYTES) throw new Error('The pending share is too large.');
    return file;
  });
  return {
    id: value.id.toLowerCase(),
    title: typeof value.title === 'string' ? value.title : '',
    text: typeof value.text === 'string' ? value.text : '',
    url: typeof value.url === 'string' ? value.url : '',
    files,
  };
}

export function installNativeShareNavigation({
  inbox = globalThis.MoaShareInbox,
  doc = globalThis.document,
} = {}) {
  if (!inbox || typeof inbox.peek !== 'function' || typeof inbox.acknowledge !== 'function') {
    return () => {};
  }
  let stopped = false;
  let reading = false;

  const pump = async () => {
    if (stopped || reading || store.get().share.status !== 'idle') return;
    reading = true;
    try {
      const result = await inbox.peek();
      if (!result?.share || stopped || store.get().share.status !== 'idle') return;
      const share = decodeNativeShare(result.share);
      presentIncomingShare(share, {
        release: async () => {
          await inbox.acknowledge(share.id);
          queueMicrotask(pump);
        },
      });
    } catch (error) {
      addToast({
        title: 'Could not open the shared item',
        detail: String(error?.message || error),
        type: 'error',
      });
    } finally {
      reading = false;
    }
  };
  const onVisibility = () => {
    if (!doc || doc.visibilityState === 'visible') pump();
  };
  doc?.addEventListener?.('visibilitychange', onVisibility);
  queueMicrotask(pump);
  return () => {
    stopped = true;
    doc?.removeEventListener?.('visibilitychange', onVisibility);
  };
}
