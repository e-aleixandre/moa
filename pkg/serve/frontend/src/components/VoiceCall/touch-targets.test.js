import { test, expect } from 'bun:test';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

const call = readFileSync(join(import.meta.dir, 'VoiceCall.css'), 'utf8');
const owners = readFileSync(join(import.meta.dir, '../Owners/OwnerRow.css'), 'utf8');

test("the owner row's call button is a 44px target in the phone density", () => {
  expect(owners).toMatch(/\.zl-side-body\.is-phone \.ow-orow-call\s*\{[^}]*width:\s*44px;[^}]*height:\s*44px/s);
});

test('Mute and Dismiss in the call bar are 44px targets on the phone', () => {
  expect(call).toMatch(/@media \(max-width: 768px\)\s*\{\s*\.vcall-icon\s*\{[^}]*width:\s*44px;[^}]*height:\s*44px/s);
});
