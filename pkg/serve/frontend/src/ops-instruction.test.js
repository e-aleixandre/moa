import { test, expect } from 'bun:test';
import { instructionOutcome, opsSessions } from './ops-instruction.js';

test('Ops targets are only concrete sessions from the verified overview', () => {
  expect(opsSessions({ projects: [{ canonical_cwd: '/work/moa', sessions: [{ id: 'one', title: 'One' }] }] }))
    .toEqual([{ id: 'one', title: 'One', project: '/work/moa', session: { id: 'one', title: 'One' } }]);
  expect(opsSessions({ projects: [{ sessions: [{ title: 'missing id' }] }] })).toEqual([]);
});

test('instruction outcomes expose safe UI states rather than response text', () => {
  expect(instructionOutcome(202, { action: 'steer', target: { id: 'one' } }).kind).toBe('steer');
  expect(instructionOutcome(409, { candidates: [{ id: 'one' }] }).kind).toBe('ambiguous');
  expect(instructionOutcome(409, null).kind).toBe('permission');
  expect(instructionOutcome(404, null).kind).toBe('no-match');
});
