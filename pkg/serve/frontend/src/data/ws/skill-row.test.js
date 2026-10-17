// skill-row.test.js — run with `bun test`
import { describe, it, expect } from 'bun:test';
import { normalizeHistory, skillRow } from './history.js';

// A user-invoked inline skill must not look like a user turn holding the whole
// SKILL.md, live or after a reload.
describe('inline skill row', () => {
  const custom = { source: 'skill', skill: 'deploy', skill_args: 'staging --fast' };
  const text = '# Deploy\n\nShip it.\n\nARGUMENTS: staging --fast';

  it('is identical live and on reload', () => {
    const live = skillRow({ msg_id: 'm1', timestamp: 5, custom }, text);
    const [reloaded] = normalizeHistory([
      { role: 'user', msg_id: 'm1', timestamp: 5, custom, content: [{ type: 'text', text }] },
    ]);
    expect(reloaded).toEqual(live);
    expect(live._type).toBe('tool_start');
    expect(live.args).toEqual({ name: 'deploy', arguments: 'staging --fast' });
    expect(live.result).toBe(text);
  });

  it('ignores anything that is not a skill invocation', () => {
    expect(skillRow({ custom: { source: 'skill_fork', skill: 'x' } }, '')).toBeNull();
    expect(skillRow({ custom: { source: 'skill' } }, '')).toBeNull();
    expect(skillRow({}, '')).toBeNull();
  });
});
