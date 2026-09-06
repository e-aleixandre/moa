// skill-fork-row.test.js — run with `bun test`
import { describe, it, expect } from 'bun:test';
import { normalizeHistory, skillForkLaunchRow } from './history.js';
import { seenJobIdsOf, liveSubagents } from '../stream-model.js';

// A slash-launched forked skill reaches the client on three lanes: as a
// user_message (agent idle), as an internal steer (agent busy), and as history
// on reload. All three must produce the same row, or the transcript changes
// shape under the user when the conversation is reloaded.
describe('skill fork launch row', () => {
  const custom = { source: 'skill_fork', subagent_job_id: 'sa-1', skill: 'probe' };

  it('is identical live and on reload', () => {
    const live = skillForkLaunchRow({ msg_id: 'm1', custom });
    const [reloaded] = normalizeHistory([
      { role: 'user', msg_id: 'm1', timestamp: 123, custom, content: [{ type: 'text', text: 'Launched ...' }] },
    ]);
    const { timestamp, ...row } = reloaded;
    expect(row).toEqual(live);
    expect(timestamp).toBe(123);
  });

  // The terminal card folds into the launch row by this exact key
  // (upsertTerminalSubagentOutcome); a mismatch draws two cards for one child.
  it('is keyed so the terminal card folds into it', () => {
    expect(skillForkLaunchRow({ custom }).tool_call_id).toBe('subagent-sa-1');
  });

  it('ignores a message that is not a fork launch', () => {
    expect(skillForkLaunchRow({ custom: { source: 'subagent' } })).toBeNull();
    expect(skillForkLaunchRow({ custom: { source: 'skill_fork' } })).toBeNull();
    expect(skillForkLaunchRow({})).toBeNull();
  });
});

// The anchor shares the `subagent-<jobId>` key with the terminal card so the two
// fold into one row. While the child still RUNS that key must not be mistaken
// for an outcome: doing so painted the live child as finished ("No result
// returned") and, because seenJobIdsOf counted it as already represented, also
// dropped its chip from the dock — the child vanished from both places at once.
describe('a running anchor is not an outcome', () => {
  const anchor = skillForkLaunchRow({
    msg_id: 'm',
    custom: { source: 'skill_fork', subagent_job_id: 'sa-1', skill: 'probe' },
  });

  it('leaves the live child visible in the dock', () => {
    const seen = seenJobIdsOf([anchor]);
    expect(seen.has('sa-1')).toBe(false);
    const { subs } = liveSubagents({ 'sa-1': { jobId: 'sa-1', status: 'running', async: true, messages: [] } }, seen);
    expect(subs.length).toBe(1);
  });

  it('still dedups the dock once the child is terminal', () => {
    const seen = seenJobIdsOf([{ ...anchor, status: 'done', result: 'ok' }]);
    expect(seen.has('sa-1')).toBe(true);
    const { subs } = liveSubagents({ 'sa-1': { jobId: 'sa-1', status: 'completed', async: true } }, seen);
    expect(subs.length).toBe(0);
  });
});
