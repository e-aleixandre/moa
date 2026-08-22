import { test, expect } from "bun:test";
import { defaultModelSpec, modelStepItems, stepBack } from "./command-palette-model.js";

const SPECS = [
  { id: "anthropic/opus", catalogId: "opus", codename: "Opus", name: "Claude Opus", provider: "anthropic", alias: "opus", accent: "mauve", sub: "4.8" },
  { id: "anthropic/haiku", catalogId: "haiku", codename: "Haiku", name: "Claude Haiku", provider: "anthropic", alias: "luna", accent: "overlay1", sub: "" },
  { id: "openai/sol", catalogId: "sol", codename: "Sol", name: "GPT Sol", provider: "openai", alias: "sol", accent: "yellow", sub: "" },
];

const labels = (items) => items.map((it) => (it.kind === "group" ? `# ${it.label}` : it.kind === "note" ? `! ${it.text}` : it.spec.id));

test("defaultModelSpec prefers the server default, else the first catalogued model", () => {
  expect(defaultModelSpec({ defaultModel: "openai/sol" }, SPECS)).toBe("openai/sol");
  expect(defaultModelSpec({}, SPECS)).toBe("anthropic/opus");
  expect(defaultModelSpec({}, [])).toBe("");
});

test("the model step opens on pinned models first, then one group per provider", () => {
  expect(labels(modelStepItems(SPECS, ["sol"], ""))).toEqual([
    "# Pinned",
    "openai/sol",
    "# anthropic",
    "anthropic/opus",
    "anthropic/haiku",
    "# openai",
    "openai/sol",
  ]);
});

test("with no pins the step is just the provider groups", () => {
  expect(labels(modelStepItems(SPECS, [], ""))[0]).toBe("# anthropic");
});

test("a query filters by codename, alias or provider into one flat results group", () => {
  expect(labels(modelStepItems(SPECS, [], "luna"))).toEqual(["# Results · 1", "anthropic/haiku"]);
  expect(labels(modelStepItems(SPECS, ["sol"], "openai"))).toEqual(["# Results · 1", "openai/sol"]);
  expect(labels(modelStepItems(SPECS, [], "opus"))).toEqual(["# Results · 1", "anthropic/opus"]);
});

test("no match leaves a non-selectable note, never an empty list", () => {
  const items = modelStepItems(SPECS, [], "zzz");
  expect(items).toHaveLength(1);
  expect(items[0].kind).toBe("note");
});

test("the model step always steps back into create, keeping the create it came from", () => {
  expect(stepBack("model", "search")).toBe("create");
  expect(stepBack("model", "create")).toBe("create");
});

test("create returns to search only when search is where the palette opened", () => {
  expect(stepBack("create", "search")).toBe("search");
  expect(stepBack("create", "create")).toBe("close");
});

test("back from the search step closes the palette", () => {
  expect(stepBack("search", "search")).toBe("close");
});
