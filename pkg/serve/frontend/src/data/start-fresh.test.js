import { describe, expect, test } from 'bun:test';
import { placeFreshMarker, settleFreshMarkers, startedFreshSinceExpiry } from './start-fresh.js';
import { normalizeHistory } from './ws/history.js';

const rows = [
  { role: 'user', _msg_id: 'u1' },
  { role: 'assistant', _msg_id: 'a1' },
  { role: 'user', _msg_id: 'u2' },
  { role: 'assistant', _msg_id: 'a2' },
];

const wire = {
  role: 'session_event',
  msg_id: 'f1',
  timestamp: 2000,
  content: [{ type: 'text', text: 'Started fresh — earlier messages are no longer sent to the model' }],
  custom: { type: 'fresh_marker', first_kept_msg_id: 'u2', tokens_before: 100000, tokens_after: 20000 },
};

describe('start fresh marker', () => {
  test('normalizes to a system row that knows the cut', () => {
    const [row] = normalizeHistory([wire]);
    expect(row).toMatchObject({ _type: 'system', systemType: 'fresh_marker', _msg_id: 'f1', firstKept: 'u2', timestamp: 2000 });
    expect(row.text).toContain('Started fresh');
  });

  test('a live marker is drawn right before the first kept message', () => {
    const [row] = normalizeHistory([wire]);
    const out = placeFreshMarker(rows, row);
    expect(out.map(m => m._msg_id)).toEqual(['u1', 'a1', 'f1', 'u2', 'a2']);
    expect(placeFreshMarker(out, row)).toBe(out);
  });

  test('falls back to the end when the cut is not loaded', () => {
    const [row] = normalizeHistory([{ ...wire, custom: { ...wire.custom, first_kept_msg_id: 'gone' } }]);
    expect(placeFreshMarker(rows, row).map(m => m._msg_id)).toEqual(['u1', 'a1', 'u2', 'a2', 'f1']);
  });

  test('a marker appended by a history delta moves to the cut', () => {
    const [row] = normalizeHistory([wire]);
    expect(settleFreshMarkers([...rows, row]).map(m => m._msg_id)).toEqual(['u1', 'a1', 'f1', 'u2', 'a2']);
  });

  test('the action is spent until a run warms the cache again', () => {
    const [row] = normalizeHistory([wire]);
    const withMarker = placeFreshMarker(rows, row);
    // Cut at t=2000s, cache expired at t=1000s: already started fresh.
    expect(startedFreshSinceExpiry(withMarker, 1000 * 1000)).toBe(true);
    // A later run moved the expiry past the marker: available again.
    expect(startedFreshSinceExpiry(withMarker, 3000 * 1000)).toBe(false);
    expect(startedFreshSinceExpiry(rows, 1000 * 1000)).toBe(false);
  });
});
