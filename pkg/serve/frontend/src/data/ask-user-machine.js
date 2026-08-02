// ask-user-machine.js — pure state machine for multi-question ask_user prompts.
// No DOM/fetch/store access: AskUserPrompt (the container) owns the wiring,
// this module owns the decisions, so both are independently testable.

// initAnswers seeds one empty answer per question.
export function initAnswers(questions) {
  return (questions || []).map(() => '');
}

// setAnswer returns a new answers array with index `idx` replaced by `val`,
// leaving every other question's answer untouched.
export function setAnswer(answers, idx, val) {
  const next = [...answers];
  next[idx] = val;
  return next;
}

// firstUnanswered returns the index of the first blank (after trimming)
// answer, or -1 if every question has an answer.
export function firstUnanswered(answers) {
  for (let i = 0; i < answers.length; i++) {
    if (!answers[i] || !answers[i].trim()) return i;
  }
  return -1;
}

// allAnswered is true iff every answer is non-blank.
export function allAnswered(answers) {
  return firstUnanswered(answers) === -1;
}

// skipAnswers fills every blank answer with the '(skipped)' sentinel,
// preserving whatever was already typed for the rest.
export function skipAnswers(questions, answers) {
  return (questions || []).map((_, i) => (answers[i] && answers[i].trim()) || '(skipped)');
}

// appendDictation adds transcribed speech to the answer at `idx`.
//
// Dictation appends rather than replaces so a long answer can be spoken in
// several passes — stop to think, carry on. When the current answer is a
// picked option it is discarded instead: reaching for the mic after choosing
// means the spoken words are the real answer.
export function appendDictation(answers, idx, text, options = []) {
  const spoken = (text || '').trim();
  if (!spoken) return answers;
  // A stray index must not extend the array with holes or set a stray property.
  if (!Number.isInteger(idx) || idx < 0 || idx >= answers.length) return answers;

  const existing = answers[idx] || '';
  const replacingOption = (options || []).includes(existing);
  if (!existing || replacingOption) return setAnswer(answers, idx, spoken);

  // Keep the user's own spacing decisions: only add a separator when the
  // existing text doesn't already end with whitespace.
  const separator = /\s$/.test(existing) ? '' : ' ';
  return setAnswer(answers, idx, existing + separator + spoken);
}
