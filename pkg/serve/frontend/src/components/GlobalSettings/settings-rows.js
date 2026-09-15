// Pure model for the settings sheet's SHAPE: which rows there are, what each
// one says on its right, and which of them push a page.
//
// Kept out of the component because that shape is the thing the migration
// decided and the thing a later change is most likely to break by accident. A
// test can state "a choice between several is a row with a caret and a page
// behind it" without a DOM.

// The identity hues, from the catalogue (zones-lab.jsx:44). Deliberately none
// of them peach: that one means "you wrote this" and may not be spent on
// decoration. The same six the project monogram uses (util/format.js:424),
// restated here rather than imported because that module keeps them private
// to the monogram and exporting them would make an internal a contract.
const HUES = [210, 265, 170, 320, 40, 190];

// The pages this sheet can push. A row whose value is a choice between several
// options opens one of these; everything else is settled on the row itself
// (a switch) or on the page it already has (the number field lives on the
// compaction page, beside the choice that makes it relevant).
export const SETTINGS_PAGES = {
  "compact-at": "Compact at",
  "compact-strategy": "Before compacting",
  "compact-model": "Summarize with",
  "subagent-models": "Subagent models",
  devices: "Devices",
};

// providerHue — the catalogue's identity hue (zones-lab.jsx:46), so a provider
// is the same colour here as its monogram is in the list. Exported for the
// component; kept here so the two never derive it differently.
export function providerHue(name) {
  let h = 0;
  const text = String(name || "");
  for (let i = 0; i < text.length; i++) h = (h * 31 + text.charCodeAt(i)) >>> 0;
  return HUES[h % HUES.length];
}

// compactAtValue — what the "Compact at" row shows on its right.
//
// The row states the THRESHOLD, not the mode: "Automatic" would name the
// setting back at itself, while "350k" is the answer to the question the row
// asks. Automatic has no number, so it says the word.
export function compactAtValue(tokens, loaded) {
  if (!loaded) return null;
  return tokens > 0 ? `${Math.round(tokens / 1000)}k` : "Automatic";
}

// strategyValue / STRATEGY_OPTIONS — what the agent gets before an automatic
// compaction. Order is by how much it intervenes.
export const STRATEGY_OPTIONS = [
  { value: "plain", label: "None", desc: "Summarize with no warning." },
  { value: "notify", label: "Warn", desc: "Warn the agent as the limit approaches, so it can save unfinished work first. Free." },
  { value: "prepare", label: "Prepare", desc: "Give the agent a full turn to write things down before summarizing. Costs a request." },
];

export function strategyValue(strategy, loaded) {
  if (!loaded) return null;
  const match = STRATEGY_OPTIONS.find((option) => option.value === strategy);
  return match ? match.label : strategy;
}

// subagentValue — the allowlist, as one word plus a count when it is limited.
// "All" is the unrestricted case and the stored list is empty for it, which is
// exactly why the row may not print "0".
export function subagentValue(allowed, total, loaded) {
  if (!loaded) return null;
  if (!allowed || allowed.length === 0) return "All";
  return `${allowed.length} of ${total}`;
}
