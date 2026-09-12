import { expect, test } from "bun:test";
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
