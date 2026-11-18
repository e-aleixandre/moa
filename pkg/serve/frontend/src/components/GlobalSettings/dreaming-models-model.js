// Pure model for the two nightly-dreaming model settings. The wire shape is
// { reader, organizer, defaults: { reader, organizer }, reader_choices: [{ spec,
// name, provider }], organizer_choices: [...] }: the models each role may use,
// already limited to the ones that fit the night's budget. A role is "" when the user picked none: the night then uses
// the default for the providers connected when it runs.

import { normalizeChoices } from "./compact-model-model.js";

export const ROLES = ["reader", "organizer"];

// chosen is the model a role will use right now, "" when it follows the default.
export function chosen(policy, role) {
  return String(policy?.[role] || "").trim();
}

// nameOf is what a spec is called to a person: the catalog name when it is
// offered, otherwise the spec itself (a hand-edited or since-disconnected one).
export function allChoices(policy) {
  return [...(policy?.reader_choices || []), ...(policy?.organizer_choices || [])];
}

export function choicesFor(policy, role) {
  return normalizeChoices(policy?.[`${role}_choices`]);
}

export function nameOf(spec, choices) {
  const value = String(spec || "").trim();
  if (!value) return "";
  return normalizeChoices(choices).find((c) => c.spec === value)?.name || value;
}

// rowValue is the right-hand reading of a role's row: the word for the default,
// the model's name for a choice.
export function rowValue(policy, role) {
  const spec = chosen(policy, role);
  return spec ? nameOf(spec, allChoices(policy)) : "Default";
}

// defaultHint says which model "Default" is right now, so the option is not a
// blank promise. Empty when the server named none (no provider connected).
export function defaultHint(policy, role) {
  const spec = String(policy?.defaults?.[role] || "").trim();
  return spec ? nameOf(spec, allChoices(policy)) : "";
}

// patchFor is the request body that sets one role and leaves the other alone.
export function patchFor(role, spec) {
  return { [role]: spec };
}

// withRole returns the policy with one role changed, for the optimistic update.
export function withRole(policy, role, spec) {
  return { ...policy, [role]: spec };
}
