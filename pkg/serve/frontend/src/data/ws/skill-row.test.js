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
    expect(live.args).toEqual({ name: 'deploy', arguments: 'staging --fast', command: '' });
    expect(live.result).toBe(text);
  });

  it('ignores anything that is not a skill invocation', () => {
    expect(skillRow({ custom: { source: 'skill_fork', skill: 'x' } }, '')).toBeNull();
    expect(skillRow({ custom: { source: 'skill' } }, '')).toBeNull();
    expect(skillRow({}, '')).toBeNull();
  });
});

import { projectStream } from '../stream-model.js';
import { normalizeBranchPoint, isJumpable } from '../rewind-model.js';

const SKILL_MD = '# Deploy\n\nShip it.';
const user = (id, text, custom, ts = 1) => ({ role: 'user', msg_id: id, timestamp: ts, custom, content: [{ type: 'text', text }] });
const asst = (id, text, ts) => ({ role: 'assistant', msg_id: id, timestamp: ts, content: [{ type: 'text', text }] });

describe('inline skill in the projected transcript', () => {
  const skill = { source: 'skill', skill: 'deploy', skill_args: 'staging', skill_command: '/deploy staging' };

  it('starts its own turn: the previous response keeps its foot', () => {
    const msgs = normalizeHistory([
      user('u1', 'first', undefined, 1), asst('a1', 'first answer', 2),
      user('u2', SKILL_MD, skill, 3), asst('a2', 'skill answer', 4),
    ]);
    const kinds = projectStream({ messages: msgs }).map(b => b.kind);
    expect(kinds).toEqual(['waypoint', 'document', 'skill', 'document']);
    const docs = projectStream({ messages: msgs }).filter(b => b.kind === 'document');
    expect(docs.map(d => d.time)).toEqual([2, 4]);
  });

  it('two consecutive skills stay separate turns', () => {
    const msgs = normalizeHistory([
      user('u1', SKILL_MD, skill, 1), asst('a1', 'one', 2),
      user('u2', SKILL_MD, skill, 3), asst('a2', 'two', 4),
    ]);
    expect(projectStream({ messages: msgs }).map(b => b.kind)).toEqual(['skill', 'document', 'skill', 'document']);
  });

  it('carries the message id and the typed command for the rewind', () => {
    const [block] = projectStream({ messages: normalizeHistory([user('u1', SKILL_MD, skill)]) });
    expect(block.msgId).toBe('u1');
    expect(block.command).toBe('/deploy staging');
  });

  it('shows a legacy transcript (custom.skill only) as the same compact row', () => {
    const msgs = normalizeHistory([user('u1', SKILL_MD, { skill: 'deploy' }), asst('a1', 'done', 2)]);
    const blocks = projectStream({ messages: msgs });
    expect(blocks.map(b => b.kind)).toEqual(['skill', 'document']);
    expect(blocks[0].command).toBe('/deploy');
  });

  it('leaves forks and other sources alone', () => {
    expect(skillRow({ custom: { source: 'owner', skill: 'x' } }, '')).toBeNull();
    expect(skillRow({ custom: { source: 'skill_fork', skill: 'x' } }, '')).toBeNull();
  });

  it('is a rewind-and-edit point in the timeline', () => {
    const p = normalizeBranchPoint({ entry_id: 'e', role: 'user', source: 'skill', is_current_path: true, label: '/deploy staging' });
    expect(p.editable).toBe(true);
    expect(isJumpable(p, 'e')).toBe(true);
    expect(normalizeBranchPoint({ entry_id: 'e', role: 'user', source: 'owner' }).editable).toBe(false);
  });
});
