import { expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { thinkingPositionFor } from "../../data/selectors.js";
import { thinkingButtonsFor } from "./ModelSelector.jsx";

const ASTRA = {
  id: "openai/gpt-6-astra",
  catalogId: "gpt-6-astra",
  name: "GPT-6 Astra",
  provider: "openai",
  reasoningEfforts: ["low", "medium", "high", "xhigh", "max"],
};

const TERRA = {
  id: "openai/gpt-5.6-terra",
  catalogId: "gpt-5.6-terra",
  name: "GPT-5.6 Terra",
  provider: "openai",
};

function selectedOption(thinking, spec, provider) {
  const value = thinkingPositionFor(thinking, spec, provider);
  return thinkingButtonsFor(spec, provider).find((option) => option.value === value);
}

test("Astra low is the zero thinking position in the picker", () => {
  // The bug this replaces: painting the effort word "low" as one filled bar,
  // which is Terra's picture and Astra's zero. The picker must select the
  // first position (bars === 0) whose label is the backend's "low".
  const option = selectedOption("low", ASTRA, "openai");
  expect(option).toMatchObject({ value: "off", label: "low", bars: 0 });
});

test("an ordinary model keeps its own level in the picker", () => {
  const option = selectedOption("medium", TERRA, "openai");
  expect(option).toMatchObject({ value: "medium", label: "med", bars: 2 });
});

// The current-model row is a STATEMENT, not a door. It used to be a button
// that jumped into its provider's page, which read as "this is what you are
// running" while behaving as "go somewhere else". Asserted against the source
// because the row has no behaviour left to call: the proof is that it renders
// no button, no chevron and no navigation handler.
test("the current model is not a control", () => {
  const src = readFileSync(new URL("./ModelSelector.jsx", import.meta.url), "utf8");
  // The slice ends where the Pinned group begins: its Edit button is the
  // next control down and is not part of the row.
  const row = src.slice(src.indexOf('<div class="zl-pick-cur">'), src.indexOf('<div class="zl-group is-pinned">'));
  expect(row).toContain('<div class="zl-pick-cur">');
  expect(row).not.toContain("<GoIcon />");
  expect(row).not.toContain("onClick");
  expect(row).not.toContain("setView(selectedSpec");
});

// Nothing may paint it as pressable either: a hover lift or a hand cursor is
// the same promise made in CSS.
test("the current model is not styled as pressable", () => {
  const css = readFileSync(new URL("./ModelSelector.css", import.meta.url), "utf8");
  expect(css).not.toContain(".zl-pick-cur:hover");
  expect(css).not.toContain(".zl-pick-cur:focus-visible");
  expect(css).toContain(".zl-pick-all { cursor: pointer; }");
});
