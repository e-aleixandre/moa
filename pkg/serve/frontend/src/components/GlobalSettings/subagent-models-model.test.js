import { expect, test } from "bun:test";
import { nextAllowedModels, scopeForAllowed } from "./subagent-models-model.js";

const MODELS = [{ id: "sol" }, { id: "terra" }, { id: "luna" }];

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
