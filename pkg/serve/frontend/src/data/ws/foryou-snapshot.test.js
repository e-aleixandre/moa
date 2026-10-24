import { describe, it, expect } from 'bun:test';
import { normalizeHistory } from './history.js';
import { projectStream } from '../stream-model.js';

// A For you snapshot is persisted so the model's request prefix survives a
// restart, but nobody wrote it: it must not come back as a user turn, a
// waypoint or anything else in the transcript.
describe('a persisted For you snapshot', () => {
  const rows = normalizeHistory([
    { role: 'user', msg_id: 'u-1', content: [{ type: 'text', text: 'hello' }] },
    {
      role: 'user',
      msg_id: 's-1',
      custom: { source: 'foryou_snapshot', snapshot_epoch: 0, durable_admission: true },
      content: [{ type: 'text', text: '<foryou_snapshot>\n<open_questions>None</open_questions>\n</foryou_snapshot>' }],
    },
  ]);

  it('is left out of the reloaded transcript', () => {
    expect(rows.map(r => r._msg_id)).not.toContain('s-1');
    expect(JSON.stringify(rows)).not.toContain('foryou_snapshot');
  });

  it('adds no waypoint of its own', () => {
    const waypoints = projectStream({ messages: rows, subagents: {} }).filter(b => b.kind === 'waypoint');
    expect(waypoints).toHaveLength(1);
    expect(waypoints[0].text).toBe('hello');
  });
});
