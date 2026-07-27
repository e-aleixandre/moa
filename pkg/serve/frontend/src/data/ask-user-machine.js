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
