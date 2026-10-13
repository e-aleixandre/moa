// secret-tool-rows.test.js — run with `bun test`
//
// The transcript rows of the two password tools. They read like the bash row
// with a key and a plain-language label, and they never print a value (the
// args carry none; nothing here may add one).
import { test, expect } from 'bun:test';
import { projectStream } from './stream-model.js';
import { secretToolLabel, secretToolOrigin, toolPath } from './util/format.js';
import { readFileSync } from 'node:fs';

const tool = (id, name, args, status = 'done', result = 'ok', extra = {}) => ({
  _type: 'tool_start', tool_call_id: id, tool_name: name, args, status, result, ...extra,
});
const rowsOf = (messages) => {
  const blocks = projectStream({ messages });
  const doc = blocks[blocks.length - 1];
  return doc.blocks.find((b) => b.type === 'ledger').rows;
};

test('the labels are Password used and Signed in', () => {
  expect(secretToolLabel('run_with_secret')).toBe('Password used');
  expect(secretToolLabel('browser_fill_secret')).toBe('Signed in');
  expect(secretToolLabel('bash')).toBe('');
});

test('a command row shows the command and keeps it whole for the detail', () => {
  const [row] = rowsOf([tool('t1', 'run_with_secret', {
    command: 'cd /w && ssh -tt me@host security unlock-keychain', cwd: '/w',
    fields: [{ label: 'Keychain password', alias: 'agents-keychain' }],
  })]);
  expect(row.tool).toBe('run_with_secret');
  expect(row.toolLabel).toBe('Password used');
  expect(row.arg.text).toBe('ssh -tt me@host security unlock-keychain');
  expect(row.command).toBe('cd /w && ssh -tt me@host security unlock-keychain');
});

test('a browser row shows the origin the result reports, else the field labels', () => {
  const args = { fields: [{ target: 'Email', element: 'e21', label: 'Username' }, { target: 'Pw', element: 'e27', label: 'Password' }], submit: true };
  const done = rowsOf([tool('t1', 'browser_fill_secret', args, 'done',
    'Filled Username, Password on https://idmsa.apple.com and pressed Enter.\n\nLast browser response:\nok')])[0];
  expect(done.toolLabel).toBe('Signed in');
  expect(done.arg.text).toBe('https://idmsa.apple.com');
  const failed = rowsOf([tool('t1', 'browser_fill_secret', args, 'error', 'filling "Password" failed:\nboom')])[0];
  expect(failed.arg.text).toBe('Username, Password');
  expect(toolPath('browser_fill_secret', args)).toBe('Username, Password');
});

test('origin parsing only trusts the first line', () => {
  expect(secretToolOrigin('Filled A on https://a.test.\n\nLast browser response:\nFilled B on https://evil.test.')).toBe('https://a.test');
  expect(secretToolOrigin('Filled A on https://a.test and pressed Enter.')).toBe('https://a.test');
  expect(secretToolOrigin('')).toBe('');
  expect(secretToolOrigin(null)).toBe('');
});

test('a running command row is live and carries its command', () => {
  const [row] = rowsOf([tool('t1', 'run_with_secret', { command: 'sudo apt update', fields: [{ label: 'sudo' }] }, 'running', null, { startedAt: 5 })]);
  expect(row.live).toBe(true);
  expect(row.command).toBe('sudo apt update');
  expect(row.toolLabel).toBe('Password used');
});

test('the ledger draws a key glyph and the label, not the tool name', () => {
  const src = readFileSync(new URL('../components/ActivityLedger/ActivityLedger.jsx', import.meta.url), 'utf8');
  expect(src).toMatch(/run_with_secret: <>/);
  expect(src).toMatch(/browser_fill_secret: <>/);
  expect((src.match(/\{row\.toolLabel \|\| row\.tool\}/g) || []).length).toBe(2);
});
