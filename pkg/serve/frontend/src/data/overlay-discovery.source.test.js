// Overlays the native Back button must be able to find. Back asks the two
// registries (data/overlays.js, data/overlay-layers.js) and closes through
// Escape, so each overlay below must be registered while open AND close on an
// Escape aimed at the document. This guards the two halves per overlay.
import { expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';

const read = (p) => readFileSync(new URL(`../${p}`, import.meta.url), 'utf8');
const code = (t) => t.replace(/\/\*[\s\S]*?\*\//g, '').replace(/(^|[^:])\/\/.*$/gm, '$1');

const CASES = [
  ['composer + / session card / preview menus (ActionMenu)', 'components/ActionMenu/ActionMenu.jsx', /registerOverlay\(idRef\.current\)[\s\S]*addEventListener\("keydown"/, /e(?:vent)?\.key === "Escape"\) onOpenChange\(false\)/],
  ['model / permission PickerSheet', 'components/ModelSelector/ModelSelector.jsx', /dismissible && onClose && !leaving \? registerOverlay\(`picker-sheet-/, /const onKey = \(e\) => \{ if \(e\.key === "Escape"\) onClose\(\); \};\n\s+document\.addEventListener/],
  ['inbox decision sheet', 'components/InboxView/InboxView.jsx', /registerOverlay\("inbox-decide"\)/, /e\.key !== "Escape"/],
];

for (const [name, file, registers, escapes] of CASES) {
  test(`${name}: registered while open`, () => {
    expect(code(read(file))).toMatch(registers);
  });
  test(`${name}: closes on a document Escape`, () => {
    expect(code(read(file))).toMatch(escapes);
  });
}
