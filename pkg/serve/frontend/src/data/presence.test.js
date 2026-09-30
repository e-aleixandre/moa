import { describe, expect, test } from 'bun:test';
import { presenceFrame, sendPresence } from './presence.js';

const doc = (state) => ({ visibilityState: state });

describe('presence', () => {
  test('reports visible only while the page is visible', () => {
    expect(JSON.parse(presenceFrame(doc('visible')))).toEqual({ type: 'presence', visible: true });
    expect(JSON.parse(presenceFrame(doc('hidden')))).toEqual({ type: 'presence', visible: false });
  });

  test('sends only on an open socket', () => {
    const sent = [];
    sendPresence({ readyState: 0, send: (m) => sent.push(m) }, doc('visible'));
    sendPresence({ readyState: 1, send: (m) => sent.push(m) }, doc('visible'));
    expect(sent).toHaveLength(1);
  });
});
