// TEMPORARY, with proto-steer-ui.js: delete both together.
import { afterEach, expect, test } from "bun:test";
import { steerUIVariant } from "./proto-steer-ui.js";

function fakeStorage(initial = {}) {
  const data = { ...initial };
  return { data, getItem: (k) => (k in data ? data[k] : null), setItem: (k, v) => { data[k] = String(v); } };
}
const realLocation = Object.getOwnPropertyDescriptor(globalThis, "location");
const realStorage = Object.getOwnPropertyDescriptor(globalThis, "localStorage");
function setEnv({ search = "", storage }) {
  Object.defineProperty(globalThis, "location", { value: { search }, configurable: true, writable: true });
  Object.defineProperty(globalThis, "localStorage", { value: storage, configurable: true, writable: true });
}
afterEach(() => {
  for (const [name, d] of [["location", realLocation], ["localStorage", realStorage]]) {
    if (d) Object.defineProperty(globalThis, name, d); else delete globalThis[name];
  }
});

test("defaults to A", () => {
  setEnv({ storage: fakeStorage() });
  expect(steerUIVariant()).toBe("a");
});

test("the URL picks the variant and remembers it for the next load", () => {
  const storage = fakeStorage();
  setEnv({ search: "?steer=b", storage });
  expect(steerUIVariant()).toBe("b");
  expect(storage.data["moa.proto.steerUI"]).toBe("b");
  setEnv({ search: "", storage });
  expect(steerUIVariant()).toBe("b");
  setEnv({ search: "?steer=a", storage });
  expect(steerUIVariant()).toBe("a");
  expect(storage.data["moa.proto.steerUI"]).toBe("a");
});

test("anything else in the URL or storage falls back to A", () => {
  setEnv({ search: "?steer=zzz", storage: fakeStorage({ "moa.proto.steerUI": "x" }) });
  expect(steerUIVariant()).toBe("a");
});

test("blocked storage does not break the page", () => {
  const storage = { getItem() { throw new Error("denied"); }, setItem() { throw new Error("denied"); } };
  setEnv({ search: "?steer=b", storage });
  expect(steerUIVariant()).toBe("b");
  setEnv({ search: "", storage });
  expect(steerUIVariant()).toBe("a");
});
