import { expect, test } from "bun:test";
import {
  allowedCount,
  createAllowedModelsWriter,
  nextAllowedModels,
  scopeForAllowed,
} from "./subagent-models-model.js";

const MODELS = [
  { catalogId: "sol", provider: "openai" },
  { catalogId: "terra", provider: "anthropic" },
  { catalogId: "luna", provider: "anthropic" },
];

test("no stored policy means every model is allowed, not none", () => {
  expect(scopeForAllowed([])).toBe("all");
  expect(scopeForAllowed(undefined)).toBe("all");
  expect(scopeForAllowed(["sol"])).toBe("selected");
});

test("toggling keeps the catalog order regardless of click order", () => {
  const afterLuna = nextAllowedModels(MODELS, [], "luna", true);
  expect(afterLuna).toEqual(["luna"]);
  expect(nextAllowedModels(MODELS, afterLuna, "sol", true)).toEqual(["sol", "luna"]);
});

test("unchecking removes only that model", () => {
  expect(nextAllowedModels(MODELS, ["sol", "terra"], "terra", false)).toEqual(["sol"]);
});

test("allowedCount counts only the group's own models", () => {
  expect(allowedCount(MODELS, ["sol", "luna"])).toBe(2);
  expect(allowedCount([{ catalogId: "terra" }], ["sol", "luna"])).toBe(0);
  expect(allowedCount(MODELS, [])).toBe(0);
});

function deferredWriter() {
  const sent = [];
  const resolvers = [];
  const applied = [];
  const errors = [];
  const writer = createAllowedModelsWriter({
    send: (ids) => {
      sent.push(ids);
      return new Promise((resolve, reject) => resolvers.push({ ids, resolve, reject }));
    },
    apply: (ids) => applied.push(ids),
    onError: (error) => errors.push(error),
  });
  return { writer, sent, resolvers, applied, errors };
}

const tick = () => new Promise((resolve) => setTimeout(resolve, 0));

// drain answers every request the writer has issued, echoing the payload back
// the way the server does, until the writer stops asking.
async function drain({ resolvers }) {
  for (let i = 0; i < 20 && resolvers.length; i += 1) {
    const pending = resolvers.shift();
    pending.resolve({ allowed_models: pending.ids });
    await tick();
  }
}

test("a burst of toggles ends on the list the user actually left on screen", async () => {
  // Regression: clicking faster than the round-trip used to send the list as
  // it looked when the click happened, so a reply to an older request
  // resurrected models the user had already turned off (6 selected, 13 saved).
  const state = deferredWriter();
  const { writer, sent } = state;
  writer.reset(["sol", "terra", "luna"]);

  writer.update((allowed) => nextAllowedModels(MODELS, allowed, "sol", false));
  writer.update((allowed) => nextAllowedModels(MODELS, allowed, "terra", false));
  writer.update((allowed) => nextAllowedModels(MODELS, allowed, "luna", true));

  await drain(state);

  expect(writer.current()).toEqual(["luna"]);
  expect(sent[sent.length - 1]).toEqual(["luna"]);
});

test("a reply that lands after a newer change does not overwrite it", async () => {
  const { writer, sent, resolvers, applied } = deferredWriter();
  writer.reset(["sol"]);

  writer.update((allowed) => nextAllowedModels(MODELS, allowed, "terra", true));
  writer.update((allowed) => nextAllowedModels(MODELS, allowed, "luna", true));
  // The server answers the first request while the second is still queued.
  resolvers[0].resolve({ allowed_models: ["sol", "terra"] });
  await tick();

  expect(writer.current()).toEqual(["sol", "terra", "luna"]);
  expect(applied.at(-1)).toEqual(["sol", "terra", "luna"]);
  expect(sent[1]).toEqual(["sol", "terra", "luna"]);
});

test("a failed write rolls back to the last confirmed policy", async () => {
  const { writer, resolvers, applied, errors } = deferredWriter();
  writer.reset(["sol", "terra"]);

  writer.update((allowed) => nextAllowedModels(MODELS, allowed, "terra", false));
  resolvers[0].reject(new Error("nope"));
  await tick();

  expect(writer.current()).toEqual(["sol", "terra"]);
  expect(applied.at(-1)).toEqual(["sol", "terra"]);
  expect(errors.length).toBe(1);
});
