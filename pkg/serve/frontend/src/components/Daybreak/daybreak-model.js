// daybreak-model.js — the selector's Daybreak rules, kept pure for tests.

// The spec that means "GPT-6 Sol with Daybreak": the server switches model and
// turns Daybreak on in one configuration change, keeping the thinking level.
export const DAYBREAK_SWITCH_SPEC = 'openai/daybreak';
export const DAYBREAK_MODEL_NAME = 'GPT-6 Sol';

// daybreakOffered: the catalog advertises Daybreak on some model, or the
// session already has it on (so it can always be turned off).
export function daybreakOffered(models, daybreak) {
  return !!daybreak || (models || []).some((m) => m.daybreakSupported);
}

// daybreakBlocks reports whether picking `model` is refused while Daybreak is
// on: an OpenAI model the known catalog does not offer it on. Other providers
// stay selectable; Daybreak just does not apply there. Without a known catalog
// nothing is blocked.
export function daybreakBlocks(model, models, daybreak) {
  if (!daybreak || !model || model.provider !== 'openai') return false;
  const known = (models || []).some((m) => m.daybreakSupported);
  return known && !model.daybreakSupported;
}

// daybreakToggle decides what turning the switch on or off does: 'set' sends
// the value; 'propose' (on, with a model the catalog rules out) changes
// nothing yet and asks to move to GPT-6 Sol.
export function daybreakToggle(on, selectedSpec, models, sessionProvider) {
  if (!on) return 'set';
  const known = (models || []).some((m) => m.daybreakSupported);
  if (!known) return 'set';
  const provider = selectedSpec?.provider || sessionProvider;
  if (selectedSpec?.daybreakSupported) return 'set';
  if (provider === 'openai' && !selectedSpec) return 'set';
  return 'propose';
}
