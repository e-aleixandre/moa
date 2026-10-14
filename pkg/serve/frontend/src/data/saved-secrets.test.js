import { test, expect, afterEach } from "bun:test";
import { loadStatuses, recordUse } from "./saved-secrets.js";

const request = { id: "r", kind: "command", command: "ssh h", actionFingerprint: "fp", fields: [{ index: 0, kind: "password", label: "Pw", alias: "h" }] };
const realError = console.error;
let logged;
afterEach(() => { console.error = realError; });
function capture() { logged = []; console.error = (...a) => logged.push(a.map(String).join(" ")); }

test("recordUse reports an outcome per saved field and never throws", async () => {
  capture();
  const bridge = {
    touch: async () => { throw Object.assign(new Error("touch boom"), { code: "x" }); },
    save: async () => { throw Object.assign(new Error("Keychain is locked"), { code: "locked" }); },
  };
  const field = request.fields[0];
  const out = await recordUse(bridge, request, { touched: [field], saves: [{ field, value: "SECRET-VALUE" }] });
  expect(out.saves).toEqual([{ alias: "h", label: "Pw", ok: false, error: "Keychain is locked" }]);
  expect(logged.length).toBe(2);
  expect(logged.join("\n")).not.toContain("SECRET-VALUE");
});

test("recordUse reports success with the label", async () => {
  const bridge = { touch: async () => ({}), save: async () => ({ ok: true }) };
  const field = request.fields[0];
  const out = await recordUse(bridge, request, { saves: [{ field, value: "v" }] });
  expect(out.saves).toEqual([{ alias: "h", label: "Pw", ok: true }]);
});

test("a failing status call is logged, not silently 'not saved'", async () => {
  capture();
  const statuses = await loadStatuses({ status: async () => { throw new Error("bridge down"); } }, request);
  expect(statuses[0]).toEqual({ saved: false, sameAction: false, savedAt: 0 });
  expect(logged.length).toBe(1);
});
