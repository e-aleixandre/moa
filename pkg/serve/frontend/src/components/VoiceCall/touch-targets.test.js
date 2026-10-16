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

test('the call bar text shrinks so the 44px icons stay inside a 360px phone', () => {
  expect(call).toMatch(/\.vcall-bar-head\s*\{[^}]*min-width:\s*0/s);
  for (const sel of ['vcall-bar-phase', 'vcall-bar-meta']) {
    const rule = call.match(new RegExp('\\.' + sel + '\\s*\\{([^}]*)\\}', 's'))[1];
    expect(rule).toMatch(/flex:\s*0 1 auto/);
    expect(rule).toMatch(/min-width:\s*0/);
    expect(rule).toMatch(/text-overflow:\s*ellipsis/);
  }
  expect(call).toMatch(/\.vcall-icon\s*\{[^}]*flex:\s*none/s);
});
