import { expect, test } from "bun:test";
import {
  groupByProvider,
  pinnedModelSpecs,
  specMatches,
  visiblePinnedSpecs,
} from "./model-selector-model.js";

const models = [
  { id: "openai/sol", catalogId: "sol", codename: "Sol", name: "GPT Sol", provider: "openai", alias: "sol" },
  { id: "openai/terra", catalogId: "terra", codename: "Terra", name: "GPT Terra", provider: "openai", alias: "terra" },
  { id: "anthropic/opus", catalogId: "opus", codename: "Opus", name: "Claude Opus", provider: "anthropic", alias: "opus" },
];

test("groupByProvider preserves catalog and model order", () => {
  expect(groupByProvider(models)).toEqual([
    { provider: "openai", items: [models[0], models[1]] },
    { provider: "anthropic", items: [models[2]] },
  ]);
});

test("pinnedModelSpecs follows stable pin order and drops unavailable IDs", () => {
  expect(pinnedModelSpecs(models, ["opus", "missing", "sol"])).toEqual([models[2], models[0]]);
});

test("visiblePinnedSpecs folds after the threshold until expanded", () => {
  expect(visiblePinnedSpecs(models, false, 2)).toEqual(models.slice(0, 2));
  expect(visiblePinnedSpecs(models, true, 2)).toEqual(models);
});

test("specMatches searches codename, display name, alias, and provider", () => {
  expect(specMatches(models[2], "opus")).toBeTrue();
  expect(specMatches(models[2], "claude")).toBeTrue();
  expect(specMatches(models[2], "anthropic")).toBeTrue();
  expect(specMatches(models[2], "terra")).toBeFalse();
});
